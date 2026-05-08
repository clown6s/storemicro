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

	"user/internal/conf"
	zaplog "user/internal/pkg/log"
	"user/internal/pkg/snowflake"

	"github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-kratos/kratos/v2/config/file"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	pkgtracing "github.com/storemicro/pkg/tracing"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	Name    = "user"
	Version string
)

func main() {
	flag.Parse()

	confPath := flagconf()

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
		Endpoint:    "10.211.55.5:4317",
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
	reg := etcd.New(etcdClient)
	helper.Infow("msg", "etcd registry connected", "endpoints", bc.Registry.Etcd.Endpoints)

	// 通过 etcd 自动分配雪花算法 node_id
	if err := snowflake.InitWithEtcd(etcdClient, Name); err != nil {
		helper.Errorf("failed to init snowflake: %v", err)
		os.Exit(1)
	}
	helper.Infow("msg", "snowflake initialized", "node_id", snowflake.NodeID)

	app, cleanup, err := wireApp(bc.Server, bc.Data, bc.Jwt, bc.Redis, zapLogger, reg)
	if err != nil {
		helper.Errorf("failed to wire app: %v", err)
		os.Exit(1)
	}
	defer cleanup()

	helper.Infow("msg", "service started", "name", Name)

	// 健康检查 HTTP sidecar（:9101），供 K8s liveness/readiness probe
	go startHealthServer(":9101", helper)

	if err := app.Run(); err != nil {
		helper.Errorf("service stopped with error: %v", err)
		os.Exit(1)
	}

	helper.Infow("msg", "service stopped", "name", Name)
}

func flagconf() string {
	var c string
	flag.StringVar(&c, "conf", "", "config path, eg: -conf config.yaml")
	if c == "" {
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
		c = candidates[0]
	}
	return c
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

// startHealthServer 启动轻量级健康检查 HTTP 服务器
func startHealthServer(addr string, helper *log.Helper) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": Name})
	})
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	helper.Infow("msg", "health server starting", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		helper.Errorf("health server error: %v", err)
	}
}
