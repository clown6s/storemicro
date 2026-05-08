package server

import (
	"time"

	districtv1 "district/api/district/v1"
	"district/internal/conf"
	"district/internal/service"

	validate "github.com/go-kratos/kratos/contrib/middleware/validate/v2"
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
	timeout := 3 * time.Second
	if t := c.Grpc.Timeout; t != nil {
		timeout = t.AsDuration()
	}
	opts := []grpc.ServerOption{
		grpc.Address(c.Grpc.Addr),
		grpc.Timeout(timeout),
		grpc.Middleware(
			recovery.Recovery(),
			tracing.Server(),
			logging.Server(logger),
			validate.ProtoValidate(),
		),
	}
	srv := grpc.NewServer(opts...)
	districtv1.RegisterDistrictServer(srv, svc)
	return srv
}
