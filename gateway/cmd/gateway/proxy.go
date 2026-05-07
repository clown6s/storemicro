package main

import (
	"context"
	"fmt"
	"net/http"

	etcdreg "github.com/go-kratos/kratos/contrib/registry/etcd/v2"
	"github.com/go-kratos/kratos/v2/transport/grpc/resolver/discovery"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	productv1 "gateway/api/product/v1"
	userv1 "gateway/api/user/v1"
)

// GatewayProxy 封装 grpc-gateway mux 和 gRPC 连接
type GatewayProxy struct {
	mux         *runtime.ServeMux
	userConn    *grpc.ClientConn
	productConn *grpc.ClientConn
	etcdClient  *clientv3.Client
}

// NewGatewayProxy 创建 grpc-gateway 代理
func NewGatewayProxy(cfg *AppConfig) (*GatewayProxy, error) {
	proxy := &GatewayProxy{}

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
	// WithInsecure(true)：etcd 中注册的 endpoint scheme 是 "grpc"（非 TLS），
	// 默认 insecure=false 会导致 resolver 查找 "grpcs" scheme，匹配不到任何 endpoint
	discoveryBuilder := discovery.NewBuilder(
		etcdRegistry,
		discovery.WithInsecure(true),
		discovery.PrintDebugLog(false),
	)

	// gRPC 连接选项
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithResolvers(discoveryBuilder),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
	}

	// 连接 user 服务（通过 etcd 服务发现）
	proxy.userConn, err = grpc.NewClient("discovery:///user", dialOpts...)
	if err != nil {
		etcdClient.Close()
		return nil, fmt.Errorf("failed to connect user service: %w", err)
	}

	// 连接 product 服务（通过 etcd 服务发现）
	proxy.productConn, err = grpc.NewClient("discovery:///product", dialOpts...)
	if err != nil {
		proxy.userConn.Close()
		etcdClient.Close()
		return nil, fmt.Errorf("failed to connect product service: %w", err)
	}

	// 创建 grpc-gateway ServeMux
	proxy.mux = runtime.NewServeMux(
		runtime.WithErrorHandler(runtime.DefaultHTTPErrorHandler),
	)

	// 注册 user 服务 HTTP→gRPC handler
	ctx := context.Background()
	if err := userv1.RegisterUserHandler(ctx, proxy.mux, proxy.userConn); err != nil {
		proxy.Close()
		return nil, fmt.Errorf("failed to register user handler: %w", err)
	}

	// 注册 product 服务 HTTP→gRPC handler
	if err := productv1.RegisterProductHandler(ctx, proxy.mux, proxy.productConn); err != nil {
		proxy.Close()
		return nil, fmt.Errorf("failed to register product handler: %w", err)
	}

	zapLog.Info("gateway proxy initialized",
		zap.String("user", "discovery:///user"),
		zap.String("product", "discovery:///product"),
	)

	return proxy, nil
}

// ServeHTTP 实现 http.Handler 接口
func (p *GatewayProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mux.ServeHTTP(w, r)
}

// Close 关闭所有连接
func (p *GatewayProxy) Close() {
	zapLog.Info("gateway proxy closing connections...")
	if p.userConn != nil {
		p.userConn.Close()
	}
	if p.productConn != nil {
		p.productConn.Close()
	}
	if p.etcdClient != nil {
		p.etcdClient.Close()
	}
}
