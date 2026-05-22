package server

import (
	"context"
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"
	sgrpc "google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	userv1 "user/api/user/v1"
	"user/internal/conf"
	"user/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	"github.com/go-kratos/kratos/v2/transport"
	kratosgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/google/wire"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
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

// #34: perIPRateLimiter 基于标准令牌桶的按 IP 速率限制（兜底防御，主防御在网关层）
type perIPRateLimiter struct {
	mu          sync.Mutex
	limiters    map[string]*ipEntry
	r           rate.Limit // 每秒产生的令牌数
	burst       int        // 令牌桶容量
	lastCleanup time.Time
	stopCh      chan struct{} // 通知 cleanupLoop 退出
}

// rateLimiter 全局引用，供 CloseRateLimiter 调用
var rateLimiter *perIPRateLimiter

// CloseRateLimiter 停止 rate limiter 的后台 cleanup goroutine（#21: 防止 goroutine 泄漏）
func CloseRateLimiter() {
	if rateLimiter != nil {
		rateLimiter.Close()
	}
}

// ipEntry 封装限流器和最后使用时间，用于定期清理
type ipEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

func newPerIPRateLimiter(rps int, burst int, cleanupInterval time.Duration) *perIPRateLimiter {
	rl := &perIPRateLimiter{
		limiters:    make(map[string]*ipEntry),
		r:           rate.Limit(rps),
		burst:       burst,
		lastCleanup: time.Now(),
		stopCh:      make(chan struct{}),
	}
	// #21: 保存全局引用供 shutdown 时关闭
	rateLimiter = rl
	// 启动后台 goroutine 定期清理过期 IP 条目，防止 map 无限增长导致 OOM
	go rl.cleanupLoop(cleanupInterval * 2) // 清理周期 = 窗口 × 2，确保不误删活跃 IP
	return rl
}

// Close 停止 cleanupLoop goroutine（#21 fix）
func (rl *perIPRateLimiter) Close() {
	close(rl.stopCh)
}

// cleanupLoop 定期清理超过 cleanupInterval 未访问的 IP 条目
func (rl *perIPRateLimiter) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			expired := 0
			for ip, entry := range rl.limiters {
				if now.Sub(entry.lastUsed) > interval {
					delete(rl.limiters, ip)
					expired++
				}
			}
			rl.mu.Unlock()
			if expired > 0 {
				zap.L().Debug("rate limiter: cleaned up expired IP entries", zap.Int("count", expired))
			}
		}
	}
}

func (rl *perIPRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()

	entry, ok := rl.limiters[ip]
	if !ok {
		rl.limiters[ip] = &ipEntry{
			limiter:  rate.NewLimiter(rl.r, rl.burst),
			lastUsed: now,
		}
		return true
	}
	entry.lastUsed = now
	return entry.limiter.Allow()
}

// rateLimitMiddleware gRPC 速率限制中间件（仅对 Login/Register 生效）
func rateLimitMiddleware(limiter *perIPRateLimiter) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			// #5 fix: 使用 transport.FromServerContext 获取 gRPC Transport 信息
			// ":method" 是 HTTP/2 伪头，不在 gRPC metadata 中，之前的行为导致限流完全失效
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return handler(ctx, req) // 无法获取 transport 信息，放行
			}
			method := tr.Operation()
			if method != "/api.user.v1.User/Login" && method != "/api.user.v1.User/Register" {
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

// customRecovery 自定义 panic recovery 中间件（记录完整堆栈，替代 Kratos 内置 recovery）
func customRecovery(logger log.Logger) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			defer func() {
				if r := recover(); r != nil {
					zap.L().Error("gRPC panic recovered",
						zap.Any("panic", r),
						zap.Stack("stack"),
					)
					err = status.Errorf(codes.Internal, "internal error")
				}
			}()
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
			customRecovery(logger),
			rateLimitMiddleware(newPerIPRateLimiter(10, 30, time.Minute)), // #34: Login/Register 10次/秒突发30
			tracing.Server(),
			logging.Server(logger),
		),
	}
	srv := kratosgrpc.NewServer(opts...)
	userv1.RegisterUserServer(srv, svc)
	return srv
}
