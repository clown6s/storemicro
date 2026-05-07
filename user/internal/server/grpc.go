package server

import (
	userv1 "user/api/user/v1"
	"user/internal/conf"
	"user/internal/pkg/jwt"
	"user/internal/pkg/redis"
	"user/internal/server/middleware"
	"user/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
)

// ProviderSet is server providers.
var ProviderSet = wire.NewSet(NewGRPCServer)

// NewGRPCServer 创建 gRPC 服务器（含 JWT+Redis 鉴权中间件）
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.UserService, j *jwt.JWT, r *redis.Redis) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Address(c.Grpc.Addr),
		grpc.Middleware(
			recovery.Recovery(),
			logging.Server(logger),
			middleware.Auth(j, r),
		),
	}
	srv := grpc.NewServer(opts...)
	userv1.RegisterUserServer(srv, svc)
	return srv
}
