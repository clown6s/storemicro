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
	pkgcmd "github.com/storemicro/pkg/cmdutil"
	pkgtracing "github.com/storemicro/pkg/tracing"
	pkglog "github.com/storemicro/pkg/zaplogger"
	"go.uber.org/zap"

	"gateway/internal/appconfig"
	"gateway/internal/auth"
	gwproxy "gateway/internal/proxy"

	districtv1 "gateway/api/district/v1"
	userv1 "gateway/api/user/v1"
)

// services 服务注册表 —— 新增服务只需在此添加一行即可
var services = []gwproxy.ServiceRegistration{
	{
		Name:            "user",
		RegisterHandler: userv1.RegisterUserHandler,
	},
	{
		Name:            "district",
		RegisterHandler: districtv1.RegisterDistrictHandler,
	},
}

// 命令行参数（只保留配置文件路径）
var configFile string

func init() {
	flag.StringVar(&configFile, "conf", "configs/config.yaml", "config file path")
}

func main() {
	flag.Parse()

	// 加载配置文件
	appCfg, err := appconfig.Load(configFile)
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
	serviceName := appCfg.Name
	if serviceName == "" {
		serviceName = "storemicro-gateway"
	}
	shutdown, err := pkgtracing.InitTracer(pkgtracing.Conf{
		ServiceName: serviceName,
		Endpoint:    appCfg.Tracing.Endpoint,
		SampleRate:  appCfg.Tracing.SampleRate,
	})
	if err != nil {
		pkglog.L().Error("failed to init jaeger tracer", zap.Error(err))
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			pkglog.L().Error("failed to shutdown jaeger tracer", zap.Error(err))
		}
	}()

	// 创建 grpc-gateway 代理
	proxy, err := gwproxy.New(appCfg, services)
	if err != nil {
		pkglog.L().Error("failed to create gateway proxy", zap.Error(err))
		os.Exit(1)
	}

	// 创建 HTTP 服务器（超时从配置读取，请求体大小限制 10MB）
	srv := &http.Server{
		Addr:              appCfg.Server.Addr,
		Handler:           limitRequestBody(newRootMux(proxy, appCfg.MetricsPassword), 10<<20),
		ReadTimeout:       appCfg.Server.ReadTimeout,
		ReadHeaderTimeout: 10 * time.Second, // 防止 slow-loris 攻击：header 读取必须在 10s 内完成
		WriteTimeout:      appCfg.Server.WriteTimeout,
		IdleTimeout:       appCfg.Server.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
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

	// 预热 HTTP 响应缓存（跳过 JWT 直接调 grpc-gateway mux，避免首个真实请求承担 MISS 开销）
	warmupCtx, warmupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer warmupCancel()
	proxy.WarmupResponseCache(warmupCtx)

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

	// 关闭 gRPC 连接（Close 内部会等待 in-flight RPC 完成）
	proxy.Close()

	pkglog.L().Info("gateway stopped")
}

// newRootMux 构建根路由：/healthz 健康检查 + /readyz 就绪检查 + /metrics + 其余全部交给 proxy
func newRootMux(proxy *gwproxy.Proxy, metricsPassword string) http.Handler {
	mux := http.NewServeMux()

	// 健康检查端点（Kubernetes liveness probe — 仅检查进程存活）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, err := json.Marshal(map[string]string{"status": "ok"})
		if err != nil {
			pkglog.L().Error("healthz json marshal failed", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	})

	// 就绪检查端点（Kubernetes readiness probe — 检查关键依赖）
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		checks := map[string]string{}

		// Redis（显式 3s 超时）
		redisCtx, redisCancel := context.WithTimeout(r.Context(), 3*time.Second)
		if err := proxy.Rdb().Ping(redisCtx).Err(); err != nil {
			redisCancel()
			pkglog.L().Warn("readyz: redis unhealthy", zap.Error(err))
			checks["redis"] = "unhealthy"
		} else {
			redisCancel()
			checks["redis"] = "ok"
		}

		// etcd — 用短超时的 KV Get 验证连通性
		etcdTimeout, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if _, etcdErr := proxy.EtcdClient().Get(etcdTimeout, "health-check-nonexistent-key"); etcdErr != nil {
			pkglog.L().Warn("readyz: etcd unhealthy", zap.Error(etcdErr))
			checks["etcd"] = "unhealthy"
		} else {
			checks["etcd"] = "ok"
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

		b, err := json.Marshal(map[string]any{"status": status, "checks": checks})
		if err != nil {
			pkglog.L().Error("readyz json marshal failed", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write(b)
	})

	// Prometheus metrics 端点（必须配置密码才能启用）
	if metricsPassword == "" {
		pkglog.L().Warn("metrics: no password configured, metrics endpoint is disabled")
		mux.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"metrics password not configured"}`))
		}))
	} else {
		mux.Handle("/metrics", pkgcmd.MetricsBearerAuth(promhttp.Handler(), metricsPassword))
	}

	// 其余所有请求走 gateway proxy（proxy 内部已有 panic recovery）
	mux.Handle("/", proxy)

	// 为根 mux 整体包裹 panic recovery，覆盖 healthz/readyz/metrics
	return auth.RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				pkglog.L().Error("panic recovered in root mux",
					zap.Any("error", rec),
					zap.Stack("stack"),
				)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":500,"message":"internal server error"}`))
			}
		}()
		mux.ServeHTTP(w, r)
	}))
}

// limitRequestBody 限制请求体大小，防止 OOM 攻击
func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}
