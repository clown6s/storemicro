package server

import (
	"context"
	"net"
	"sync"
	"time"

	sgrpc "google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	userv1 "user/api/user/v1"
	"user/internal/conf"
	"user/internal/service"

	validate "github.com/go-kratos/kratos/contrib/middleware/validate/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	kratosgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// ProviderSet is server providers.
var ProviderSet = wire.NewSet(NewGRPCServer)

const (
	// #32: 消息大小限制常量
	defaultMaxRecvMsgSize = 4 << 20  // 4MB
	defaultMaxSendMsgSize = 16 << 20 // 16MB
)

// #34: perIPRateLimiter 基于内存的按 IP 速率限制（兜底防御，主防御在网关层）
type perIPRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*tokenBucket
	rate     int           // 每秒允许的请求数
	burst    int           // 突发容量
	window   time.Duration // 滑动窗口
}

type tokenBucket struct {
	count    int
	lastTime time.Time
}

func newPerIPRateLimiter(rate, burst int, window time.Duration) *perIPRateLimiter {
	return &perIPRateLimiter{
		limiters: make(map[string]*tokenBucket),
		rate:     rate,
		burst:    burst,
		window:   window,
	}
}

func (rl *perIPRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()

	bucket, ok := rl.limiters[ip]
	if !ok || now.Sub(bucket.lastTime) > rl.window {
		rl.limiters[ip] = &tokenBucket{count: 1, lastTime: now}
		return true
	}
	bucket.count++
	bucket.lastTime = now
	return bucket.count <= rl.burst
}

// rateLimitMiddleware gRPC 速率限制中间件（仅对 Login/Register 生效）
func rateLimitMiddleware(limiter *perIPRateLimiter) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			// 从 gRPC metadata 获取 method（兼容直连和网关转发场景）
			md, _ := metadata.FromIncomingContext(ctx)
			method := md.Get(":method") // Kratos/gRPC 标准 metadata key
			if len(method) == 0 {
				return handler(ctx, req) // 无法确定方法名，放行
			}
			if method[0] != "/api.user.v1.User/Login" && method[0] != "/api.user.v1.User/Register" {
				return handler(ctx, req)
			}
			var ip string
			if p, ok := peer.FromContext(ctx); ok {
				ip, _, _ = net.SplitHostPort(p.Addr.String())
			}
			if ip == "" {
				ip = "unknown"
			}
			if !limiter.allow(ip) {
				return nil, status.Error(codes.ResourceExhausted, "too many requests")
			}
			return handler(ctx, req)
		}
	}
}

// NewGRPCServer 创建 gRPC 服务器（鉴权由网关统一完成，内部服务无需重复校验）
func NewGRPCServer(c *conf.Server, logger log.Logger, svc *service.UserService) *kratosgrpc.Server {
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
			rateLimitMiddleware(newPerIPRateLimiter(10, 30, time.Minute)), // #34: Login/Register 10次/分突发30次
		),
	}
	srv := kratosgrpc.NewServer(opts...)
	userv1.RegisterUserServer(srv, svc)
	return srv
}
