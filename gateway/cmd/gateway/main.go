package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	pkgtracing "github.com/storemicro/pkg/tracing"
	"go.uber.org/zap"
)

func main() {
	flag.Parse()

	// 加载配置文件
	appCfg, err := LoadAppConfig(configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config %s: %v\n", configFile, err)
		os.Exit(1)
	}

	// 初始化 zap 日志，同时桥接 Kratos log（供 resolver 等内部组件使用）
	kratosLogger := NewZapLogger(appCfg.Log.Level)
	defer zapLog.Sync()
	log.SetLogger(kratosLogger)

	// 初始化链路追踪（OTLP gRPC → Jaeger）
	shutdown, err := pkgtracing.InitTracer(pkgtracing.Conf{
		ServiceName: "storemicro-gateway",
		Endpoint:    appCfg.Tracing.Endpoint,
		SampleRate:  appCfg.Tracing.SampleRate,
	})
	if err != nil {
		zapLog.Fatal("failed to init jaeger tracer", zap.Error(err))
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			zapLog.Error("failed to shutdown jaeger tracer", zap.Error(err))
		}
	}()

	// 创建 grpc-gateway 代理
	proxy, err := NewGatewayProxy(appCfg)
	if err != nil {
		zapLog.Fatal("failed to create gateway proxy", zap.Error(err))
	}
	defer proxy.Close()

	// 创建 HTTP 服务器（生产级超时配置）
	srv := &http.Server{
		Addr:         appCfg.Server.Addr,
		Handler:      newRootMux(proxy),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	zapLog.Info("storemicro gateway starting",
		zap.String("listen", appCfg.Server.Addr),
		zap.String("config", configFile),
		zap.Strings("etcd", appCfg.Etcd.Endpoints),
		zap.String("log_level", appCfg.Log.Level),
	)

	// 启动 HTTP 服务器（非阻塞）
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zapLog.Fatal("gateway error", zap.Error(err))
		}
	}()

	// 等待信号优雅退出
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	zapLog.Info("gateway shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		zapLog.Error("gateway forced shutdown", zap.Error(err))
	}

	zapLog.Info("gateway stopped")
}

// newRootMux 构建根路由：/healthz 健康检查 + /metrics + 其余全部交给 proxy
func newRootMux(proxy *GatewayProxy) http.Handler {
	mux := http.NewServeMux()

	// 健康检查端点（Kubernetes liveness/readiness probe）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Prometheus metrics 端点
	mux.Handle("/metrics", promhttp.Handler())

	// 其余所有请求走 gateway proxy
	mux.Handle("/", proxy)

	return mux
}
