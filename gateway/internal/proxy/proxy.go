package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	etcdreg "github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2/transport/grpc/resolver/discovery"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	pkgretry "github.com/storemicro/pkg/retry"
	pkglog "github.com/storemicro/pkg/zaplogger"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/health"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"gateway/internal/appconfig"
	"gateway/internal/auth"
	"gateway/internal/circuitbreaker"
)

// ServiceRegistration 描述一个后端 gRPC 服务的注册信息
type ServiceRegistration struct {
	Name            string
	RegisterHandler func(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error
}

// Proxy 封装 grpc-gateway mux 和 gRPC 连接
type Proxy struct {
	mux            *runtime.ServeMux
	conns          []*grpc.ClientConn
	etcdClient     *clientv3.Client
	rdb            *redis.Client
	jwtSecret      string
	origins        []string
	proxies        []string
	otelHandler    http.Handler
	requestTimeout time.Duration // 单次 gRPC 请求超时
	closeOnce      sync.Once     // #14: 防止 Close 被重复调用
}

// New 创建 grpc-gateway 代理
func New(cfg *appconfig.AppConfig, services []ServiceRegistration) (*Proxy, error) {
	p := &Proxy{
		jwtSecret:      cfg.Jwt.Secret,
		origins:        cfg.Server.AllowOrigins,
		proxies:        cfg.Server.TrustedProxies,
		requestTimeout: cfg.GrpcClient.RequestTimeout,
	}

	// 创建 Redis 客户端（连接池配置）
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.Db,
		PoolSize:     20,
		MinIdleConns: 5,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		MaxRetries:   3,
	})

	// 验证 Redis 连接（带重试，30s 整体超时）
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer redisCancel()
	if err := pkgretry.Do(redisCtx, pkgretry.DefaultConfig(), "redis",
		func(rCtx context.Context) error {
			pingCtx, pingCancel := context.WithTimeout(rCtx, 5*time.Second)
			defer pingCancel()
			return rdb.Ping(pingCtx).Err()
		},
	); err != nil {
		return nil, fmt.Errorf("failed to connect redis after retries: %w", err)
	}
	p.rdb = rdb
	pkglog.L().Info("redis connected", zap.String("addr", cfg.Redis.Addr))

	// 创建 etcd client（用于服务发现，带重试，60s 整体超时）
	var etcdClient *clientv3.Client
	etcdCtx, etcdCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer etcdCancel()
	if err := pkgretry.Do(etcdCtx, pkgretry.DefaultConfig(), "etcd",
		func(rCtx context.Context) error {
			if etcdClient != nil {
				etcdClient.Close()
			}
			var err error
			etcdClient, err = clientv3.New(clientv3.Config{
				Endpoints:   cfg.Etcd.Endpoints,
				DialTimeout: cfg.Etcd.DialTimeout,
				Username:    cfg.Etcd.Username,
				Password:    cfg.Etcd.Password,
			})
			return err
		},
	); err != nil {
		return nil, fmt.Errorf("failed to connect etcd after retries: %w", err)
	}
	p.etcdClient = etcdClient

	etcdRegistry := etcdreg.New(etcdClient)
	discoveryBuilder := discovery.NewBuilder(
		etcdRegistry,
		discovery.WithInsecure(true),
		discovery.PrintDebugLog(false),
	)

	// gRPC 连接选项（keepalive + otel + retry + 熔断器）
	cbCfg := &circuitbreaker.Config{
		MaxRequests:  cfg.GrpcClient.CircuitBreaker.MaxRequests,
		Interval:     cfg.GrpcClient.CircuitBreaker.Interval,
		Timeout:      cfg.GrpcClient.CircuitBreaker.Timeout,
		FailureRatio: cfg.GrpcClient.CircuitBreaker.FailureRatio,
		MinRequests:  cfg.GrpcClient.CircuitBreaker.MinRequests,
	}

	serviceCfg, err := cfg.GrpcClient.BuildServiceConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build gRPC service config: %w", err)
	}

	// 构建服务名列表（供熔断器白名单使用）
	serviceNames := make([]string, 0, len(services))
	for _, svc := range services {
		serviceNames = append(serviceNames, svc.Name)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(discoveryBuilder),
		grpc.WithDefaultServiceConfig(serviceCfg),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithUnaryInterceptor(circuitbreaker.UnaryInterceptor(cbCfg, serviceNames)),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(cfg.GrpcClient.MaxRecvMsgSize*1024*1024),
			grpc.MaxCallSendMsgSize(cfg.GrpcClient.MaxSendMsgSize*1024*1024),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	}

	p.mux = runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{
				UseProtoNames: false, // JSON 输出使用 camelCase（如 pageSize）而非 snake_case（如 page_size）
			},
			UnmarshalOptions: protojson.UnmarshalOptions{
				DiscardUnknown: false,
			},
		}),
		runtime.WithErrorHandler(customErrorHandler),
		runtime.WithRoutingErrorHandler(customRoutingErrorHandler),
	)

	// RegisterHandler 使用带超时的 context，防止启动时卡死
	regCtx, regCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer regCancel()

	for _, svc := range services {
		conn, err := grpc.NewClient("discovery:///"+svc.Name, dialOpts...)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("failed to connect %s service: %w", svc.Name, err)
		}
		p.conns = append(p.conns, conn)

		if err := svc.RegisterHandler(regCtx, p.mux, conn); err != nil {
			p.Close()
			return nil, fmt.Errorf("failed to register %s handler: %w", svc.Name, err)
		}

		pkglog.L().Info("service registered", zap.String("service", svc.Name))
	}

	// 预构建 otel handler（panic recovery → OpenTelemetry → CORS → security headers → JWT 认证 → grpc-gateway mux）
	p.otelHandler = otelhttp.NewHandler(
		auth.RecoverMiddleware(auth.CORSMiddleware(p.origins)(auth.SecurityHeadersMiddleware(auth.JWTAuth(p.jwtSecret, p.rdb, p.proxies, p.mux)))),
		"gateway",
	)

	return p, nil
}

// ServeHTTP 实现 http.Handler 接口
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 为每个请求设置独立的 gRPC 超时（从配置读取），防止后端挂起时阻塞到 HTTP WriteTimeout
	ctx, cancel := context.WithTimeout(r.Context(), p.requestTimeout)
	defer cancel()
	p.otelHandler.ServeHTTP(w, r.WithContext(ctx))
}

// Rdb 返回 Redis 客户端（供健康检查使用）
func (p *Proxy) Rdb() *redis.Client {
	return p.rdb
}

// EtcdClient 返回 etcd 客户端（供健康检查使用）
func (p *Proxy) EtcdClient() *clientv3.Client {
	return p.etcdClient
}

// Conns 返回 gRPC 连接列表（供优雅关闭 drain 使用）
func (p *Proxy) Conns() []*grpc.ClientConn {
	return p.conns
}

// Close 关闭所有连接（#14: sync.Once 保证仅执行一次）
func (p *Proxy) Close() {
	p.closeOnce.Do(func() {
		pkglog.L().Info("gateway proxy closing connections...")
		for _, conn := range p.conns {
			if conn != nil {
				if err := conn.Close(); err != nil {
					pkglog.L().Warn("failed to close gRPC connection", zap.Error(err))
				}
			}
		}
		p.conns = nil
		if p.rdb != nil {
			if err := p.rdb.Close(); err != nil {
				pkglog.L().Warn("failed to close redis connection", zap.Error(err))
			}
		}
		if p.etcdClient != nil {
			if err := p.etcdClient.Close(); err != nil {
				pkglog.L().Warn("failed to close etcd client", zap.Error(err))
			}
		}
	})
}

// customErrorHandler 将 gRPC 错误转为统一的 JSON 格式 HTTP 响应（#12: 不泄露上游错误详情）
func customErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	st, ok := status.FromError(err)
	if !ok {
		// 非 gRPC 错误，返回通用 500
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"message":"internal server error"}`))
		return
	}

	httpStatus := runtime.HTTPStatusFromCode(st.Code())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	// 仅返回状态码和通用消息，不暴露上游 gRPC 错误详情
	_, _ = w.Write([]byte(fmt.Sprintf(`{"code":%d,"message":"%s"}`, httpStatus, http.StatusText(httpStatus))))
}

// customRoutingErrorHandler 未匹配路由时返回 JSON 格式 404（替代默认纯文本）
func customRoutingErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch status {
	case http.StatusMethodNotAllowed:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"code":405,"message":"method not allowed"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
	}
}
