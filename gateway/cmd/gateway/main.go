package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-kratos/kratos/v2/log"
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
		Endpoint:    "10.211.55.5:4317",
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

	// 创建 HTTP 服务器
	srv := &http.Server{
		Addr:    appCfg.Server.Addr,
		Handler: proxy,
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		zapLog.Error("gateway forced shutdown", zap.Error(err))
	}

	zapLog.Info("gateway stopped")
}
