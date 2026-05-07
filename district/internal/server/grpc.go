package server

import (
	districtv1 "district/api/district/v1"
	"district/internal/conf"
	"district/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(NewGRPCServer)

// NewGRPCServer 创建 gRPC 服务器
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.DistrictService) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Address(c.Grpc.Addr),
		grpc.Middleware(
			recovery.Recovery(),
			tracing.Server(),
			logging.Server(logger),
		),
	}
	srv := grpc.NewServer(opts...)
	districtv1.RegisterDistrictServer(srv, svc)
	return srv
}
