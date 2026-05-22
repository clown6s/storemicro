package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"district/internal/conf"

	stdlog "log"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	pkgcmd "github.com/storemicro/pkg/cmdutil"
	envexpand "github.com/storemicro/pkg/envexpand"
	pkghealth "github.com/storemicro/pkg/health"
	pkgretry "github.com/storemicro/pkg/retry"
	pkgtracing "github.com/storemicro/pkg/tracing"
	pkglog "github.com/storemicro/pkg/zaplogger"

	etcd2 "github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	Name    = "district"
	Version = "dev" // #33: 构建时通过 -ldflags 覆盖，默认 "dev"
)

// init 在 flag.Parse() 之前注册 -conf 标志
var confPath string

func init() {
	flag.StringVar(&confPath, "conf", "", "config path, eg: -conf config.yaml")
}

func main() {
	flag.Parse()

	if confPath == "" {
		confPath = pkgcmd.ResolveConfDir()
	}

	// 抑制 Kratos config.Load() 内部标准库 log 的 DEBUG 输出
	stdlog.SetOutput(io.Discard)

	// 抑制 Kratos config 包内部日志（config loaded、watcher's ctx cancel 等）
	log.SetLogger(pkglog.NewNopLogger())

	// 加载配置文件（自动展开 ${VAR:default} 环境变量）
	c := config.New(
		config.WithSource(
			envexpand.NewSource(confPath),
		),
	)
	if err := c.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config from %s: %v\n", confPath, err)
		os.Exit(1)
	}

	var bc conf.Bootstrap
	if err := c.Scan(&bc); err != nil {
		fmt.Fprintf(os.Stderr, "failed to scan config from %s: %v\n", confPath, err)
		os.Exit(1)
	}

	// 校验必填配置段
	if bc.Server == nil || bc.Server.Grpc == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [server.grpc] section")
		os.Exit(1)
	}
	if bc.Registry == nil || bc.Registry.Etcd == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [registry.etcd] section")
		os.Exit(1)
	}
	if bc.Logger == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [logger] section")
		os.Exit(1)
	}
	if bc.Data == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [data] section")
		os.Exit(1)
	}
	if bc.Tracing == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [tracing] section")
		os.Exit(1)
	}

	// 初始化 zap logger（支持 dev/prod 模式、文件轮转）
	logCfg := pkglog.Config{
		Level:      bc.Logger.Level,
		Mode:       os.Getenv("LOG_MODE"), // dev | prod，空 = dev
		Filepath:   os.Getenv("LOG_FILE"), // 日志文件路径，空 = 仅 stdout
		MaxSize:    100,                   // 默认 100MB
		MaxBackups: 10,                    // 默认保留 10 份
		MaxAge:     30,                    // 默认保留 30 天
		Compress:   os.Getenv("LOG_COMPRESS") == "true",
	}
	zapLogger := pkglog.New(logCfg)
	log.SetLogger(zapLogger)
	defer pkglog.L().Sync()

	// #28: 配置加载完成后恢复 stdlog 输出，避免全局抑制第三方包日志
	stdlog.SetOutput(os.Stderr)

	helper := log.NewHelper(zapLogger)
	helper.Infow("msg", "service initializing",
		"name", Name, "version", Version,
		"conf", confPath,
		"grpc_addr", bc.Server.Grpc.Addr,
		"log_level", bc.Logger.Level,
	)

	// 初始化链路追踪（OTLP gRPC → Jaeger）
	shutdown, err := pkgtracing.InitTracer(pkgtracing.Conf{
		ServiceName: "storemicro-" + Name,
		Endpoint:    bc.Tracing.Endpoint,
		SampleRate:  bc.Tracing.SampleRate,
	})
	if err != nil {
		helper.Errorf("failed to init jaeger tracer: %v", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			helper.Errorf("failed to shutdown jaeger tracer: %v", err)
		}
	}()

	// 创建 etcd client 和 registrar（带重试，60s 整体超时）
	var etcdClient *clientv3.Client
	etcdCtx, etcdCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer etcdCancel()
	if err := pkgretry.Do(etcdCtx, pkgretry.DefaultConfig(), "etcd",
		func(rCtx context.Context) error {
			if etcdClient != nil {
				etcdClient.Close()
			}
			// etcd Timeout 防御性校验：0 或负值回退到 5 秒
			etcdTimeout := bc.Registry.Etcd.Timeout
			if etcdTimeout <= 0 {
				etcdTimeout = 5
			}
			var err error
			etcdClient, err = clientv3.New(clientv3.Config{
				Endpoints:   bc.Registry.Etcd.Endpoints,
				DialTimeout: time.Duration(etcdTimeout) * time.Second,
			})
			return err
		},
	); err != nil {
		helper.Errorf("failed to connect etcd after retries: %v", err)
		os.Exit(1)
	}
	defer etcdClient.Close()
	reg := etcd2.New(etcdClient)
	helper.Infow("msg", "etcd registry connected", "endpoints", bc.Registry.Etcd.Endpoints)

	result, cleanup, err := wireApp(bc.Server, bc.Data, zapLogger, reg)
	if err != nil {
		helper.Errorf("failed to wire app: %v", err)
		os.Exit(1)
	}
	defer cleanup()

	// 预热省市区缓存（启动时从 DB 加载全量数据到 atomic.Value）
	warmupCtx, warmupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	result.UseCase.Warmup(warmupCtx)
	// 预热全量树缓存（构建一次树，后续请求直接返回）
	result.Service.WarmupTreeCache(warmupCtx)
	warmupCancel()
	defer result.UseCase.Close()

	helper.Infow("msg", "service started", "name", Name)

	// 健康检查 HTTP sidecar（默认 :9103，可通过 HEALTH_PORT 环境变量覆盖）
	healthAddr := pkghealth.ValidateHealthPort(os.Getenv("HEALTH_PORT"), ":9103")

	// Prometheus metrics 端点（必须配置 METRICS_PASSWORD 才启用，未配置时返回 401）
	var metricsHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"metrics endpoint requires METRICS_PASSWORD env"}`))
	})
	if metricsPassword := os.Getenv("METRICS_PASSWORD"); metricsPassword != "" {
		metricsHandler = pkgcmd.MetricsBearerAuth(promhttp.Handler(), metricsPassword)
	}

	healthCfg := pkghealth.Config{
		Port:        healthAddr,
		ServiceName: Name,
		ReadinessChecks: []pkghealth.ReadinessCheck{
			func(ctx context.Context) error {
				return result.AppData.DB().PingContext(ctx)
			},
		},
		MetricsHandler: metricsHandler,
	}
	healthSrv, err := pkghealth.StartHealthServer(healthCfg)
	if err != nil {
		helper.Errorf("start health server failed: %v", err)
		os.Exit(1)
	}

	// 启动服务（阻塞，内部监听信号并优雅退出）
	if err := result.App.Run(); err != nil {
		helper.Errorf("failed to run app: %v", err)
		os.Exit(1)
	}

	// 优雅关闭健康检查服务器
	if healthSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := healthSrv.Shutdown(ctx); err != nil {
			helper.Warnf("health server shutdown error: %v", err)
		}
	}
}

func newApp(logger log.Logger, gs *grpc.Server, reg registry.Registrar) *kratos.App {
	return kratos.New(
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(map[string]string{}),
		kratos.Logger(logger),
		kratos.Server(gs),
		kratos.Registrar(reg),
	)
}
