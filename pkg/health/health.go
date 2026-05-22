package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"
)

// ReadinessCheck readiness check 函数类型
type ReadinessCheck func(ctx context.Context) error

// Config health server 配置
type Config struct {
	Port            string           // e.g., ":8081"
	ServiceName     string           // 服务名称（用于 healthz 响应）
	ReadinessChecks []ReadinessCheck // readiness check 函数列表
	MetricsHandler  http.Handler     // Prometheus metrics handler（nil 则不启用）
}

// StartHealthServer 启动健康检查 HTTP server，返回 *http.Server 供优雅关闭
func StartHealthServer(cfg Config) (*http.Server, error) {
	// 校验端口格式
	if _, portStr, err := net.SplitHostPort(cfg.Port); err != nil {
		return nil, fmt.Errorf("invalid HEALTH_PORT format %q: %w", cfg.Port, err)
	} else if portNum, err := strconv.Atoi(portStr); err != nil || portNum < 1 || portNum > 65535 {
		return nil, fmt.Errorf("invalid HEALTH_PORT %q: port must be 1-65535", cfg.Port)
	}

	mux := http.NewServeMux()

	// /healthz — liveness probe（仅检查进程存活）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, err := json.Marshal(map[string]string{"status": "ok", "service": cfg.ServiceName})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	})

	// /readyz — readiness probe（检查所有注册的 readiness check）
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		allOk := true
		for _, check := range cfg.ReadinessChecks {
			if err := check(ctx); err != nil {
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
		b, err := json.Marshal(map[string]string{"status": status})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write(b)
	})

	// /metrics — Prometheus metrics（可选）
	if cfg.MetricsHandler != nil {
		mux.Handle("/metrics", cfg.MetricsHandler)
	}

	// panic recovery 中间件
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				log.Printf("[PANIC] health server: %v\n%s", rec, stack)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":500,"message":"internal server error"}`))
			}
		}()
		mux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              cfg.Port,
		Handler:           handler,
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.Port)
	if err != nil {
		return nil, fmt.Errorf("health server listen %s: %w", cfg.Port, err)
	}

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			_ = err // logged by caller on Shutdown
		}
	}()

	return srv, nil
}

// ValidateHealthPort 校验 HEALTH_PORT 环境变量格式，返回规范化后的地址
func ValidateHealthPort(envVal, defaultPort string) string {
	if envVal == "" {
		return defaultPort
	}
	// 兼容纯端口号格式（如 "9101"）
	if _, _, err := net.SplitHostPort(envVal); err != nil {
		envVal = ":" + envVal
	}
	return envVal
}
