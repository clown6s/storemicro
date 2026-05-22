package circuitbreaker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sony/gobreaker"
	pkglog "github.com/storemicro/pkg/zaplogger"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// Config 熔断器配置（引用 appconfig 包的类型会导致循环依赖，所以重新定义）
type Config struct {
	MaxRequests  int
	Interval     int // 秒
	Timeout      int // 秒
	FailureRatio float64
	MinRequests  int
}

// store 按服务名隔离的熔断器存储
type store struct {
	mu              sync.RWMutex
	cbs             map[string]*gobreaker.CircuitBreaker
	allowedServices map[string]bool // 服务名白名单，防止 map 无限增长
	cfg             *Config
	defaultCB       *gobreaker.CircuitBreaker // 未知服务名使用的通用熔断器
}

// onStateChange 熔断器状态变更回调（共享逻辑）
func onStateChange(name string, from, to gobreaker.State) {
	pkglog.L().Warn("circuit breaker state changed",
		zap.String("service", name),
		zap.String("from", from.String()),
		zap.String("to", to.String()),
	)
}

// newCircuitBreaker 创建熔断器实例（抽取公共构造逻辑）
func newCircuitBreaker(name string, c *Config) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: uint32(c.MaxRequests),
		Interval:    time.Duration(c.Interval) * time.Second,
		Timeout:     time.Duration(c.Timeout) * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < uint32(c.MinRequests) {
				return false
			}
			return float64(counts.TotalFailures)/float64(counts.Requests) >= c.FailureRatio
		},
		OnStateChange: onStateChange,
	})
}

// applyDefaults 填充零值为合理默认值
func applyDefaults(c *Config) {
	if c.MaxRequests <= 0 {
		c.MaxRequests = 5
	}
	if c.Interval <= 0 {
		c.Interval = 10
	}
	if c.Timeout <= 0 {
		c.Timeout = 30
	}
	if c.FailureRatio <= 0 {
		c.FailureRatio = 0.5
	}
	if c.FailureRatio > 1 {
		c.FailureRatio = 1.0
	}
	if c.MinRequests <= 0 {
		c.MinRequests = 10
	}
}

var (
	globalCB   *store
	initCBOnce sync.Once
)

// initStore 初始化熔断器存储（由 Interceptor 在首次调用时自动触发）
// serviceNames 为已注册的服务名白名单，防止 map 无限增长
func initStore(cfg *Config, serviceNames []string) {
	initCBOnce.Do(func() {
		// 复制配置，避免修改调用者的原始结构体
		c := *cfg
		applyDefaults(&c)
		// 动态构建服务名白名单
		allowed := make(map[string]bool, len(serviceNames))
		for _, name := range serviceNames {
			allowed[name] = true
		}
		globalCB = &store{
			cbs:             make(map[string]*gobreaker.CircuitBreaker),
			allowedServices: allowed,
			cfg:             &c,
			defaultCB:       newCircuitBreaker("grpc-default", &c),
		}
	})
}

func (s *store) getCB(serviceName string) *gobreaker.CircuitBreaker {
	// 防御性 nil 保护，防止 store 未初始化时 panic
	if s == nil {
		pkglog.L().Error("circuit breaker: store is nil (not initialized)")
		return gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:        "grpc-fallback-nil",
			MaxRequests: 5,
			Interval:    10 * time.Second,
			Timeout:     30 * time.Second,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.Requests >= 10 && float64(counts.TotalFailures)/float64(counts.Requests) >= 0.5
			},
		})
	}

	// 未知服务名返回通用熔断器，防止 map 无限增长
	// allowedServices 仅在 Init 中写入，sync.Once 保证 happens-before，此处无需加锁
	if !s.allowedServices[serviceName] {
		pkglog.L().Warn("circuit breaker: unknown service name, using default",
			zap.String("service", serviceName))
		return s.defaultCB
	}

	s.mu.RLock()
	cb, ok := s.cbs[serviceName]
	s.mu.RUnlock()
	if ok {
		return cb
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if cb, ok = s.cbs[serviceName]; ok {
		return cb
	}
	cb = newCircuitBreaker(fmt.Sprintf("grpc-%s", serviceName), s.cfg)
	s.cbs[serviceName] = cb
	return cb
}

// extractServiceName 从 gRPC 方法名解析服务名
// 输入格式: "/package.service.version.ServiceName/Method"
// 输出: "servicename"（小写，与注册名一致）
func extractServiceName(method string) string {
	method = strings.TrimPrefix(method, "/")
	// 取最后一个 '/' 之前的部分作为 service path
	if idx := strings.LastIndex(method, "/"); idx >= 0 {
		method = method[:idx]
	}
	// method = "api.user.v1.User" → 取最后一个 '.' 之后
	if idx := strings.LastIndex(method, "."); idx >= 0 {
		return strings.ToLower(method[idx+1:])
	}
	return strings.ToLower(method)
}

// UnaryInterceptor 返回按服务隔离的 gRPC 一元拦截器
func UnaryInterceptor(cfg *Config, serviceNames []string) grpc.UnaryClientInterceptor {
	initStore(cfg, serviceNames)

	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		serviceName := extractServiceName(method)
		cb := globalCB.getCB(serviceName)
		_, err := cb.Execute(func() (interface{}, error) {
			return nil, invoker(ctx, method, req, reply, cc, opts...)
		})
		return err
	}
}

// StreamInterceptor 返回按服务隔离的 gRPC 流式拦截器
func StreamInterceptor(cfg *Config, serviceNames []string) grpc.StreamClientInterceptor {
	initStore(cfg, serviceNames)

	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		serviceName := extractServiceName(method)
		cb := globalCB.getCB(serviceName)
		var clientStream grpc.ClientStream
		_, err := cb.Execute(func() (interface{}, error) {
			var streamErr error
			clientStream, streamErr = streamer(ctx, desc, cc, method, opts...)
			return nil, streamErr
		})
		return clientStream, err
	}
}
