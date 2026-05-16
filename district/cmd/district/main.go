package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"district/internal/conf"
	"district/internal/data"
	zaplog "district/internal/pkg/log"
	stdlog "log"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	envexpand "github.com/storemicro/pkg/envexpand"
	pkgretry "github.com/storemicro/pkg/retry"
	pkgtracing "github.com/storemicro/pkg/tracing"

	etcd2 "github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/config"
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

	// 自动加载 .env 文件（配置目录 → 当前目录 → 上级目录），不覆盖已有环境变量
	loadDotenv(confPath)

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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			helper.Errorf("failed to shutdown jaeger tracer: %v", err)
		}
	}()

	// 创建 etcd client 和 registrar（带重试）
	var etcdClient *clientv3.Client
	if err := pkgretry.Do(context.Background(), pkgretry.DefaultConfig(), "etcd",
		func(rCtx context.Context) error {
			if etcdClient != nil {
				etcdClient.Close()
			}
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
	reg := etcd2.New(etcdClient)
	helper.Infow("msg", "etcd registry connected", "endpoints", bc.Registry.Etcd.Endpoints)

	app, appData, cleanup, err := wireApp(bc.Server, bc.Data, zapLogger, reg)
	if err != nil {
		helper.Errorf("failed to wire app: %v", err)
		os.Exit(1)
	}
	defer cleanup()

	helper.Infow("msg", "service started", "name", Name)

	// 健康检查 HTTP sidecar（:9103）
	healthSrv := startHealthServer(":9103", helper, appData)

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
func startHealthServer(addr string, helper *log.Helper, appData *data.Data) *http.Server {
	mux := http.NewServeMux()
	// liveness — 仅检查进程存活
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, err := json.Marshal(map[string]string{"status": "ok", "service": Name})
		if err != nil {
			helper.Errorf("healthz json marshal failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	})
	// readiness — 检查 MySQL 连通性
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		checks := map[string]string{}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if err := appData.DB().PingContext(ctx); err != nil {
			helper.Warnf("readyz mysql ping failed: %v", err)
			checks["mysql"] = "unhealthy"
		} else {
			checks["mysql"] = "ok"
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
			helper.Errorf("readyz json marshal failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write(b)
	})
	// Prometheus metrics 端点（可选 Bearer token 认证）
	metricsHandler := http.Handler(promhttp.Handler())
	if metricsPassword := os.Getenv("METRICS_PASSWORD"); metricsPassword != "" {
		metricsHandler = metricsBearerAuth(promhttp.Handler(), metricsPassword)
	}
	mux.Handle("/metrics", metricsHandler)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	helper.Infow("msg", "health server starting", "addr", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			helper.Errorf("health server error: %v", err)
		}
	}()
	return srv
}

// metricsBearerAuth 包装 handler，要求 Bearer token 才放行（timing-safe 比较）
func metricsBearerAuth(next http.Handler, password string) http.Handler {
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

// unquoteValue 剥离配对的引号（"value" 或 'value'），不配对时返回原值
func unquoteValue(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// loadDotenv 自动加载 .env 文件（配置目录 → 当前目录 → 上级目录），不覆盖已有环境变量
func loadDotenv(confPath string) {
	// 优先在配置文件所在目录找 .env
	candidates := []string{filepath.Join(confPath, ".env"), ".env", "../.env"}
	if confPath != "" {
		if info, err := os.Stat(confPath); err == nil && info.IsDir() {
			candidates[0] = filepath.Join(confPath, ".env")
		} else {
			candidates[0] = filepath.Join(filepath.Dir(confPath), ".env")
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
				val = unquoteValue(val)
				if os.Getenv(key) == "" {
					os.Setenv(key, val)
				}
			}
			return
		}
	}
}
