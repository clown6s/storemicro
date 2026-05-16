package proxy

import (
	"context"
	"fmt"
	"net/http"
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
	mux         *runtime.ServeMux
	conns       []*grpc.ClientConn
	etcdClient  *clientv3.Client
	rdb         *redis.Client
	jwtSecret   string
	origins     []string
	proxies     []string
	otelHandler http.Handler
}

// New 创建 grpc-gateway 代理
func New(cfg *appconfig.AppConfig, services []ServiceRegistration) (*Proxy, error) {
	p := &Proxy{
		jwtSecret: cfg.Jwt.Secret,
		origins:   cfg.Server.AllowOrigins,
		proxies:   cfg.Server.TrustedProxies,
	}

	// 创建 Redis 客户端（连接池配置）
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.Db,
		PoolSize:     20,
		MinIdleConns: 5,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		MaxRetries:   3,
	})

	if err := pkgretry.Do(context.Background(), pkgretry.DefaultConfig(), "redis",
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

	// 创建 etcd client（用于服务发现，带重试）
	var etcdClient *clientv3.Client
	if err := pkgretry.Do(context.Background(), pkgretry.DefaultConfig(), "etcd",
		func(rCtx context.Context) error {
			if etcdClient != nil {
				etcdClient.Close()
			}
			var err error
			etcdClient, err = clientv3.New(clientv3.Config{
				Endpoints:   cfg.Etcd.Endpoints,
				DialTimeout: cfg.Etcd.DialTimeout,
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

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(discoveryBuilder),
		grpc.WithDefaultServiceConfig(cfg.GrpcClient.BuildServiceConfig()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithUnaryInterceptor(circuitbreaker.UnaryInterceptor(cbCfg)),
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
		runtime.WithErrorHandler(runtime.DefaultHTTPErrorHandler),
	)

	ctx := context.Background()

	for _, svc := range services {
		conn, err := grpc.NewClient("discovery:///"+svc.Name, dialOpts...)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("failed to connect %s service: %w", svc.Name, err)
		}
		p.conns = append(p.conns, conn)

		if err := svc.RegisterHandler(ctx, p.mux, conn); err != nil {
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
	p.otelHandler.ServeHTTP(w, r)
}

// Rdb 返回 Redis 客户端（供健康检查使用）
func (p *Proxy) Rdb() *redis.Client {
	return p.rdb
}

// EtcdClient 返回 etcd 客户端（供健康检查使用）
func (p *Proxy) EtcdClient() *clientv3.Client {
	return p.etcdClient
}

// Close 关闭所有连接
func (p *Proxy) Close() {
	pkglog.L().Info("gateway proxy closing connections...")
	for _, conn := range p.conns {
		if conn != nil {
			conn.Close()
		}
	}
	p.conns = nil
	if p.rdb != nil {
		p.rdb.Close()
	}
	if p.etcdClient != nil {
		p.etcdClient.Close()
	}
}
