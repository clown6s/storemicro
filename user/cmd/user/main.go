package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	stdlog "log"
	"user/internal/conf"
	"user/internal/data"
	zaplog "user/internal/pkg/log"
	"user/internal/pkg/snowflake"

	"github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	envexpand "github.com/storemicro/pkg/envexpand"
	pkgretry "github.com/storemicro/pkg/retry"
	pkgtracing "github.com/storemicro/pkg/tracing"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	Name     = "user"
	Version  string
	confPath string
)

func init() {
	flag.StringVar(&confPath, "conf", "", "config path, eg: -conf config.yaml")
}

func main() {
	flag.Parse()

	if confPath == "" {
		confPath = resolveConfDir()
	}

	// 抑制 Kratos config.Load() 内部标准库 log 的 DEBUG 输出
	stdlog.SetOutput(io.Discard)

	// 抑制 Kratos config 包内部日志（config loaded、watcher's ctx cancel 等）
	log.SetLogger(zaplog.NewNopLogger())

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
		fmt.Fprintf(os.Stderr, "failed to scan config: %v\n", err)
		os.Exit(1)
	}

	// 初始化 zap logger（支持 dev/prod 模式、文件轮转）
	logCfg := zaplog.Config{
		Level:      bc.Logger.Level,
		Mode:       os.Getenv("LOG_MODE"), // dev | prod，空 = dev
		Filepath:   os.Getenv("LOG_FILE"), // 日志文件路径，空 = 仅 stdout
		MaxSize:    100,                   // 默认 100MB
		MaxBackups: 10,                    // 默认保留 10 份
		MaxAge:     30,                    // 默认保留 30 天
		Compress:   os.Getenv("LOG_COMPRESS") == "true",
	}
	zapLogger := zaplog.NewZapLogger(logCfg)
	log.SetLogger(zapLogger)

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
		if err := shutdown(context.Background()); err != nil {
			helper.Errorf("failed to shutdown jaeger tracer: %v", err)
		}
	}()

	// 创建 etcd client 和 registrar（带重试）
	var etcdClient *clientv3.Client
	if err := pkgretry.Do(context.Background(), pkgretry.DefaultConfig(), "etcd",
		func(rCtx context.Context) error {
			var err error
			etcdClient, err = clientv3.New(clientv3.Config{
				Endpoints:   bc.Registry.Etcd.Endpoints,
				DialTimeout: time.Duration(bc.Registry.Etcd.Timeout) * time.Second,
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
	helper.Infow("msg", "snowflake initialized", "node_id", snowflake.NodeID)

	app, appData, cleanup, err := wireApp(bc.Server, bc.Data, bc.Jwt, bc.Redis, zapLogger, reg)
	if err != nil {
		helper.Errorf("failed to wire app: %v", err)
		os.Exit(1)
	}
	defer cleanup()

	helper.Infow("msg", "service started", "name", Name)

	// 健康检查 HTTP sidecar（:9101），供 K8s liveness/readiness probe
	healthSrv := startHealthServer(":9101", helper, appData)

	// 启动服务（阻塞，内部监听信号并优雅退出）
	if err := app.Run(); err != nil {
		helper.Errorf("failed to run app: %v", err)
		os.Exit(1)
	}

	// 优雅关闭健康检查服务器
	if healthSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = healthSrv.Shutdown(ctx)
	}
}

func resolveConfDir() string {
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(exeDir, "configs"),
		filepath.Join(exeDir, "..", "configs"),
		filepath.Join("configs"),
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
	}
	return candidates[0]
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

// startHealthServer 启动轻量级健康检查 HTTP 服务器（含 Prometheus metrics），返回 server 引用供优雅关闭
func startHealthServer(addr string, helper *log.Helper, appData *data.Data) *http.Server {
	mux := http.NewServeMux()
	// liveness — 仅检查进程存活
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, _ := json.Marshal(map[string]string{"status": "ok", "service": Name})
		_, _ = w.Write(b)
	})
	// readiness — 检查 MySQL 和 Redis 连通性
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		checks := map[string]string{}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		// MySQL
		if err := appData.DB().PingContext(ctx); err != nil {
			checks["mysql"] = "unhealthy: " + err.Error()
		} else {
			checks["mysql"] = "ok"
		}

		// Redis
		if appData.Redis() != nil {
			if err := appData.Redis().Ping(ctx); err != nil {
				checks["redis"] = "unhealthy: " + err.Error()
			} else {
				checks["redis"] = "ok"
			}
		}

		allOk := true
		for _, v := range checks {
			if v != "ok" {
				allOk = false
				break
			}
		}
		status := "ok"
		code := http.StatusOK
		if !allOk {
			status = "not ready"
			code = http.StatusServiceUnavailable
		}
		b, _ := json.Marshal(map[string]any{"status": status, "checks": checks})
		w.WriteHeader(code)
		_, _ = w.Write(b)
	})
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	helper.Infow("msg", "health server starting", "addr", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			helper.Errorf("health server error: %v", err)
		}
	}()
	return srv
}
