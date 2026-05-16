package server

import (
	"time"

	sgrpc "google.golang.org/grpc"

	userv1 "user/api/user/v1"
	"user/internal/conf"
	"user/internal/service"

	validate "github.com/go-kratos/kratos/contrib/middleware/validate/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	kratosgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
)

// ProviderSet is server providers.
var ProviderSet = wire.NewSet(NewGRPCServer)

// NewGRPCServer 创建 gRPC 服务器（鉴权由网关统一完成，内部服务无需重复校验）
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.UserService) *kratosgrpc.Server {
	if c.Grpc == nil {
		panic("grpc server config must not be nil, check config.yaml server.grpc section")
	}
	timeout := 3 * time.Second
	if t := c.Grpc.Timeout; t != nil {
		timeout = t.AsDuration()
	}
	opts := []kratosgrpc.ServerOption{
		kratosgrpc.Address(c.Grpc.Addr),
		kratosgrpc.Timeout(timeout),
		// 限制接收消息大小（默认 4MB），防止超大请求导致 OOM
		kratosgrpc.Options(sgrpc.MaxRecvMsgSize(4 << 20)),
		kratosgrpc.Middleware(
			recovery.Recovery(),
			tracing.Server(),
			logging.Server(logger),
			validate.ProtoValidate(),
		),
	}
	srv := kratosgrpc.NewServer(opts...)
	userv1.RegisterUserServer(srv, svc)
	return srv
}
