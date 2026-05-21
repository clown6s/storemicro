package server

import (
	"time"

	sgrpc "google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	districtv1 "district/api/district/v1"
	"district/internal/conf"
	"district/internal/service"

	validate "github.com/go-kratos/kratos/contrib/middleware/validate/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	kratosgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(NewGRPCServer)

const (
	// #32: 消息大小限制常量
	defaultMaxRecvMsgSize = 4 << 20  // 4MB
	defaultMaxSendMsgSize = 16 << 20 // 16MB
)

// NewGRPCServer 创建 gRPC 服务器
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.DistrictService) *kratosgrpc.Server {
	// #31: Wire 要求构造函数返回单值（非 error），无法优雅处理配置错误。
	// 生产环境应确保 config.yaml server.grpc 段正确配置，避免触发此 panic。
	if c.Grpc == nil {
		log.Errorf("grpc server config is nil, check config.yaml server.grpc section")
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
		kratosgrpc.Options(sgrpc.MaxRecvMsgSize(defaultMaxRecvMsgSize)),
		// 限制发送消息大小（默认 unlimited），防止超大响应导致客户端 OOM
		kratosgrpc.Options(sgrpc.MaxSendMsgSize(defaultMaxSendMsgSize)),
		// 连接保活：10 分钟最大连接年龄，防止连接泄漏
		kratosgrpc.Options(sgrpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge: 10 * time.Minute,
		})),
		// 防止恶意客户端频繁发送 PING 帧导致 DoS
		kratosgrpc.Options(sgrpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		})),
		kratosgrpc.Middleware(
			recovery.Recovery(),
			tracing.Server(),
			logging.Server(logger),
			validate.ProtoValidate(),
		),
	}
	srv := kratosgrpc.NewServer(opts...)
	districtv1.RegisterDistrictServer(srv, svc)
	return srv
}
