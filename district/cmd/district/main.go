package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"district/internal/conf"
	zaplog "district/internal/pkg/log"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	pkgtracing "github.com/storemicro/pkg/tracing"

	etcd2 "github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-kratos/kratos/v2/config/file"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	Name    = "district"
	Version string
)

// init 在 flag.Parse() 之前注册 -conf 标志
var confPath string

func init() {
	flag.StringVar(&confPath, "conf", "", "config path, eg: -conf config.yaml")
}

func main() {
	flag.Parse()

	if confPath == "" {
		confPath = resolveConfDir()
	}

	c := config.New(
		config.WithSource(
			file.NewSource(confPath),
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

	// 初始化 zap logger（仅控制台输出）
	zapLogger := zaplog.NewZapLogger(bc.Logger.Level)
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

	// 创建 etcd client 和 registrar
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   bc.Registry.Etcd.Endpoints,
		DialTimeout: time.Duration(bc.Registry.Etcd.Timeout) * time.Second,
	})
	if err != nil {
		helper.Errorf("failed to connect etcd: %v", err)
		os.Exit(1)
	}
	defer etcdClient.Close()
	reg := etcd2.New(etcdClient)
	helper.Infow("msg", "etcd registry connected", "endpoints", bc.Registry.Etcd.Endpoints)

	app, cleanup, err := wireApp(bc.Server, bc.Data, zapLogger, reg)
	if err != nil {
		helper.Errorf("failed to wire app: %v", err)
		os.Exit(1)
	}
	defer cleanup()

	helper.Infow("msg", "service started", "name", Name)

	// 健康检查 HTTP sidecar（:9103）
	healthSrv := startHealthServer(":9103", helper)

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

// startHealthServer 启动轻量级健康检查 HTTP 服务器（含 Prometheus metrics）
func startHealthServer(addr string, helper *log.Helper) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, _ := json.Marshal(map[string]string{"status": "ok", "service": Name})
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
