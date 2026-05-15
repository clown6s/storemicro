package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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

	// 自动加载 .env 文件（配置文件目录 → 当前目录 → 上级目录）
	loadDotenv(configFile)

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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			pkglog.L().Error("failed to shutdown jaeger tracer", zap.Error(err))
		}
	}()

	// 创建 grpc-gateway 代理
	proxy, err := NewGatewayProxy(appCfg)
	if err != nil {
		pkglog.L().Error("failed to create gateway proxy", zap.Error(err))
		os.Exit(1)
	}

	// 创建 HTTP 服务器（生产级超时配置 + 请求体大小限制）
	srv := &http.Server{
		Addr:           appCfg.Server.Addr,
		Handler:        limitRequestBody(newRootMux(proxy, appCfg.MetricsPassword), 10<<20),
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
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

	// 先优雅关闭 HTTP 服务器（等待在途请求完成），再关闭后端连接
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		pkglog.L().Error("gateway forced shutdown", zap.Error(err))
	}

	// 所有在途请求完成后，关闭后端连接
	proxy.Close()

	pkglog.L().Info("gateway stopped")
}

// newRootMux 构建根路由：/healthz 健康检查 + /readyz 就绪检查 + /metrics + 其余全部交给 proxy
func newRootMux(proxy *GatewayProxy, metricsPassword string) http.Handler {
	mux := http.NewServeMux()

	// 健康检查端点（Kubernetes liveness probe — 仅检查进程存活）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, _ := json.Marshal(map[string]string{"status": "ok"})
		_, _ = w.Write(b)
	})

	// 就绪检查端点（Kubernetes readiness probe — 检查关键依赖）
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		checks := map[string]string{}

		// Redis（显式 3s 超时）
		redisCtx, redisCancel := context.WithTimeout(r.Context(), 3*time.Second)
		if err := proxy.rdb.Ping(redisCtx).Err(); err != nil {
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
		_, etcdErr := proxy.etcdClient.Get(etcdTimeout, "health-check-nonexistent-key")
		if etcdErr != nil {
			// 过滤掉正常的 "key not found" 错误（说明连接成功）
			if !strings.Contains(etcdErr.Error(), "key not found") {
				pkglog.L().Warn("readyz: etcd unhealthy", zap.Error(etcdErr))
				checks["etcd"] = "unhealthy"
			} else {
				checks["etcd"] = "ok"
			}
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

		b, _ := json.Marshal(map[string]any{"status": status, "checks": checks})
		w.WriteHeader(code)
		_, _ = w.Write(b)
	})

	// Prometheus metrics 端点（需要 metrics 密钥认证，防止暴露内部指标）
	if metricsPassword != "" {
		mux.Handle("/metrics", metricsAuth(promhttp.Handler(), metricsPassword))
	} else {
		mux.Handle("/metrics", promhttp.Handler())
	}

	// 其余所有请求走 gateway proxy
	mux.Handle("/", proxy)

	return mux
}

// loadDotenv 自动加载 .env 文件（配置文件目录 → 当前目录 → 上级目录），不覆盖已有环境变量
func loadDotenv(configFile string) {
	candidates := []string{filepath.Join(configFile, ".env"), ".env", "../.env"}
	if configFile != "" {
		if info, err := os.Stat(configFile); err == nil && info.IsDir() {
			candidates[0] = filepath.Join(configFile, ".env")
		} else {
			candidates[0] = filepath.Join(filepath.Dir(configFile), ".env")
		}
	}
	for _, p := range candidates {
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
				// 去除引号包裹的值（兼容 KEY="value" 和 KEY='value'）
				val = strings.Trim(val, `"'`)
				if os.Getenv(key) == "" {
					os.Setenv(key, val)
				}
			}
			return
		}
	}
}

// metricsAuth 包装 handler，要求 Authorization header 中带 "Bearer {password}" 才放行（timing-safe 比较）
func metricsAuth(next http.Handler, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(password)) != 1 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitRequestBody 限制请求体大小，防止 OOM 攻击
func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}
