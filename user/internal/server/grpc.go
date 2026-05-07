package server

import (
	userv1 "user/api/user/v1"
	"user/internal/conf"
	"user/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
)

// ProviderSet is server providers.
var ProviderSet = wire.NewSet(NewGRPCServer)

// NewGRPCServer 创建 gRPC 服务器（鉴权由网关统一完成，内部服务无需重复校验）
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.UserService) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Address(c.Grpc.Addr),
		grpc.Middleware(
			recovery.Recovery(),
			tracing.Server(),
			logging.Server(logger),
		),
	}
	srv := grpc.NewServer(opts...)
	userv1.RegisterUserServer(srv, svc)
	return srv
}
