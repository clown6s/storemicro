package server

import (
	productv1 "product/api/product/v1"
	"product/internal/conf"
	"product/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
)

// ProviderSet is server providers.
var ProviderSet = wire.NewSet(NewGRPCServer)

// NewGRPCServer 创建 gRPC 服务器
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.ProductService) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Address(c.Grpc.Addr),
		grpc.Middleware(
			recovery.Recovery(),
			tracing.Server(),
			logging.Server(logger),
		),
	}
	srv := grpc.NewServer(opts...)
	productv1.RegisterProductServer(srv, svc)
	return srv
}
