package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	stdlog "log"
	"user/internal/conf"
	"user/internal/pkg/snowflake"
	"user/internal/server"

	pkgcmd "github.com/storemicro/pkg/cmdutil"
	envexpand "github.com/storemicro/pkg/envexpand"
	pkghealth "github.com/storemicro/pkg/health"
	pkgretry "github.com/storemicro/pkg/retry"
	pkgtracing "github.com/storemicro/pkg/tracing"
	pkglog "github.com/storemicro/pkg/zaplogger"

	"github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	Name     = "user"
	Version  = "dev" // #33: 构建时通过 -ldflags 覆盖，默认 "dev"
	confPath string
)

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
	if bc.Jwt == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [jwt] section")
		os.Exit(1)
	}
	if bc.Data == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [data] section")
		os.Exit(1)
	}
	if bc.Redis == nil {
		fmt.Fprintln(os.Stderr, "error: config missing [redis] section")
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
			var err error
			// etcd Timeout 防御性校验：0 或负值回退到 5 秒
			etcdTimeout := bc.Registry.Etcd.Timeout
			if etcdTimeout <= 0 {
				etcdTimeout = 5
			}
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
	reg := etcd.New(etcdClient)
	helper.Infow("msg", "etcd registry connected", "endpoints", bc.Registry.Etcd.Endpoints)

	// 通过 etcd 自动分配雪花算法 node_id
	if err := snowflake.InitWithEtcd(etcdClient, Name); err != nil {
		helper.Errorf("failed to init snowflake: %v", err)
		os.Exit(1)
	}
	defer snowflake.Shutdown()
	helper.Infow("msg", "snowflake initialized", "node_id", snowflake.NodeID)

	result, cleanup, err := wireApp(bc.Server, bc.Data, bc.Jwt, bc.Redis, zapLogger, reg)
	if err != nil {
		helper.Errorf("failed to wire app: %v", err)
		os.Exit(1)
	}
	defer cleanup()

	helper.Infow("msg", "service started", "name", Name)

	// 健康检查 HTTP sidecar（默认 :9101，可通过 HEALTH_PORT 环境变量覆盖）
	healthAddr := pkghealth.ValidateHealthPort(os.Getenv("HEALTH_PORT"), ":9101")

	var metricsHandler http.Handler
	if metricsPassword := os.Getenv("METRICS_PASSWORD"); metricsPassword != "" {
		metricsHandler = pkgcmd.MetricsBearerAuth(promhttp.Handler(), metricsPassword)
	} else {
		helper.Warn("metrics: no METRICS_PASSWORD configured, /metrics endpoint is disabled")
	}

	healthCfg := pkghealth.Config{
		Port:        healthAddr,
		ServiceName: Name,
		ReadinessChecks: []pkghealth.ReadinessCheck{
			func(ctx context.Context) error {
				return result.AppData.DB().PingContext(ctx)
			},
			func(ctx context.Context) error {
				rdb := result.AppData.Redis()
				if rdb == nil {
					return nil // Redis 未配置时不视为 unhealthy
				}
				return rdb.Ping(ctx)
			},
		},
		MetricsHandler: metricsHandler,
	}
	healthSrv, err := pkghealth.StartHealthServer(healthCfg)
	if err != nil {
		helper.Errorf("start health server failed: %v", err)
		os.Exit(1)
	}

	defer func() {
		if healthSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := healthSrv.Shutdown(ctx); err != nil {
				helper.Warnf("health server shutdown error: %v", err)
			}
		}
	}()

	// 启动服务（阻塞，内部监听信号并优雅退出）
	if err := result.App.Run(); err != nil {
		helper.Errorf("failed to run app: %v", err)
		os.Exit(1)
	}

	// 关闭 rate limiter 的 cleanup goroutine（防止 goroutine 泄漏）
	server.CloseRateLimiter()
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
