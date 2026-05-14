package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	pkgtracing "github.com/storemicro/pkg/tracing"
	pkglog "github.com/storemicro/pkg/zaplogger"
	"go.uber.org/zap"
)

func main() {
	flag.Parse()

	// 自动加载当前目录及上级目录的 .env 文件
	loadDotenv()

	// 加载配置文件
	appCfg, err := LoadAppConfig(configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config %s: %v\n", configFile, err)
		os.Exit(1)
	}

	// 初始化 zap 日志（支持 dev/prod 模式、文件轮转），同时桥接 Kratos log
	logCfg := pkglog.Config{
		Level:      appCfg.Log.Level,
		Mode:       appCfg.Log.Mode,
		Filepath:   appCfg.Log.Filepath,
		MaxSize:    appCfg.Log.MaxSize,
		MaxBackups: appCfg.Log.MaxBackups,
		MaxAge:     appCfg.Log.MaxAge,
		Compress:   appCfg.Log.Compress,
	}
	if err := pkglog.Validate(logCfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid log config: %v\n", err)
		os.Exit(1)
	}
	kratosLogger := pkglog.New(logCfg)
	defer pkglog.L().Sync()
	log.SetLogger(kratosLogger)

	// 初始化链路追踪（OTLP gRPC → Jaeger）
	shutdown, err := pkgtracing.InitTracer(pkgtracing.Conf{
		ServiceName: "storemicro-gateway",
		Endpoint:    appCfg.Tracing.Endpoint,
		SampleRate:  appCfg.Tracing.SampleRate,
	})
	if err != nil {
		pkglog.L().Error("failed to init jaeger tracer", zap.Error(err))
		os.Exit(1)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			pkglog.L().Error("failed to shutdown jaeger tracer", zap.Error(err))
		}
	}()

	// 创建 grpc-gateway 代理
	proxy, err := NewGatewayProxy(appCfg)
	if err != nil {
		pkglog.L().Error("failed to create gateway proxy", zap.Error(err))
		os.Exit(1)
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

	pkglog.L().Info("storemicro gateway starting",
		zap.String("listen", appCfg.Server.Addr),
		zap.String("config", configFile),
		zap.Strings("etcd", appCfg.Etcd.Endpoints),
		zap.String("log_level", appCfg.Log.Level),
		zap.String("log_mode", appCfg.Log.Mode),
	)

	// 启动 HTTP 服务器（非阻塞）
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			pkglog.L().Error("gateway error", zap.Error(err))
		}
	}()

	// 等待信号优雅退出
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	pkglog.L().Info("gateway shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		pkglog.L().Error("gateway forced shutdown", zap.Error(err))
	}

	pkglog.L().Info("gateway stopped")
}

// newRootMux 构建根路由：/healthz 健康检查 + /metrics + 其余全部交给 proxy
func newRootMux(proxy *GatewayProxy) http.Handler {
	mux := http.NewServeMux()

	// 健康检查端点（Kubernetes liveness/readiness probe）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, _ := json.Marshal(map[string]string{"status": "ok"})
		_, _ = w.Write(b)
	})

	// Prometheus metrics 端点
	mux.Handle("/metrics", promhttp.Handler())

	// 其余所有请求走 gateway proxy
	mux.Handle("/", proxy)

	return mux
}

// loadDotenv 自动加载 .env 文件（当前目录 → 上级目录），不覆盖已有环境变量
func loadDotenv() {
	for _, p := range []string{".env", "../.env"} {
		if data, err := os.ReadFile(p); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				key, val, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				key = strings.TrimSpace(key)
				val = strings.TrimSpace(val)
				if os.Getenv(key) == "" {
					os.Setenv(key, val)
				}
			}
			return
		}
	}
}
