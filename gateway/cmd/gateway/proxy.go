package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	etcdreg "github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2/transport/grpc/resolver/discovery"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// ServiceRegistration 描述一个后端 gRPC 服务的注册信息
type ServiceRegistration struct {
	// Name 服务名，用于 etcd 服务发现（如 "user"、"product"）
	Name string
	// RegisterHandler 将 gRPC 连接注册到 grpc-gateway mux 的函数
	RegisterHandler func(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error
}

// GatewayProxy 封装 grpc-gateway mux 和 gRPC 连接
type GatewayProxy struct {
	mux          *runtime.ServeMux
	conns        []*grpc.ClientConn
	etcdClient   *clientv3.Client
	rdb          *redis.Client
	jwtSecret    string
	allowOrigins []string
	otelHandler  http.Handler
}

// NewGatewayProxy 创建 grpc-gateway 代理
func NewGatewayProxy(cfg *AppConfig) (*GatewayProxy, error) {
	proxy := &GatewayProxy{
		jwtSecret:    cfg.Jwt.Secret,
		allowOrigins: cfg.Server.AllowOrigins,
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
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect redis: %w", err)
	}
	proxy.rdb = rdb
	zapLog.Info("redis connected", zap.String("addr", cfg.Redis.Addr))

	// 创建 etcd client（用于服务发现）
	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Etcd.Endpoints,
		DialTimeout: cfg.Etcd.DialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect etcd: %w", err)
	}
	proxy.etcdClient = etcdClient

	// 创建 kratos etcd registry（实现 registry.Discovery）
	etcdRegistry := etcdreg.New(etcdClient)

	// 用 discovery.NewBuilder 将 registry 包装为 gRPC resolver.Builder
	discoveryBuilder := discovery.NewBuilder(
		etcdRegistry,
		discovery.WithInsecure(true),
		discovery.PrintDebugLog(false),
	)

	// gRPC 连接选项（keepalive 保活 + otel 追踪 + retry 重试）
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(discoveryBuilder),
		grpc.WithDefaultServiceConfig(`{
			"loadBalancingPolicy":"round_robin",
			"methodConfig": [{
				"name": [{}],
				"retryPolicy": {
					"maxAttempts": 3,
					"initialBackoff": "0.1s",
					"maxBackoff": "1s",
					"backoffMultiplier": 2.0,
					"retryableStatusCodes": ["UNAVAILABLE", "RESOURCE_EXHAUSTED"]
				}
			}]
		}`),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithUnaryInterceptor(circuitBreakerUnaryInterceptor()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                20 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	// 创建 grpc-gateway ServeMux
	proxy.mux = runtime.NewServeMux(
		runtime.WithErrorHandler(runtime.DefaultHTTPErrorHandler),
	)

	ctx := context.Background()

	// 逐个连接并注册服务
	for _, svc := range services {
		conn, err := grpc.NewClient("discovery:///"+svc.Name, dialOpts...)
		if err != nil {
			proxy.Close()
			return nil, fmt.Errorf("failed to connect %s service: %w", svc.Name, err)
		}
		proxy.conns = append(proxy.conns, conn)

		if err := svc.RegisterHandler(ctx, proxy.mux, conn); err != nil {
			proxy.Close()
			return nil, fmt.Errorf("failed to register %s handler: %w", svc.Name, err)
		}

		zapLog.Info("service registered", zap.String("service", svc.Name))
	}

	// 预构建 otelhttp handler（避免每次请求重新 wrap）
	proxy.otelHandler = otelhttp.NewHandler(
		corsMiddleware(proxy.allowOrigins)(jwtAuth(proxy.jwtSecret, proxy.rdb, proxy.mux)),
		"gateway",
	)

	return proxy, nil
}

// ServeHTTP 实现 http.Handler 接口（OpenTelemetry → JWT 认证 → grpc-gateway mux）
func (p *GatewayProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.otelHandler.ServeHTTP(w, r)
}

// Close 关闭所有连接
func (p *GatewayProxy) Close() {
	zapLog.Info("gateway proxy closing connections...")
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
