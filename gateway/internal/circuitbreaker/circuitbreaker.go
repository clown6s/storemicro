package circuitbreaker

import (
	"context"
	"fmt"
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
	mu  sync.RWMutex
	cbs map[string]*gobreaker.CircuitBreaker
	cfg *Config
}

var (
	globalCB   *store
	initCBOnce sync.Once
)

// Init 初始化熔断器存储（应用启动时调用一次）
func Init(cfg *Config) {
	initCBOnce.Do(func() {
		if cfg.MaxRequests <= 0 {
			cfg.MaxRequests = 5
		}
		if cfg.Interval <= 0 {
			cfg.Interval = 10
		}
		if cfg.Timeout <= 0 {
			cfg.Timeout = 30
		}
		if cfg.FailureRatio <= 0 {
			cfg.FailureRatio = 0.5
		}
		if cfg.FailureRatio > 1 {
			cfg.FailureRatio = 1.0
		}
		if cfg.MinRequests <= 0 {
			cfg.MinRequests = 10
		}
		globalCB = &store{
			cbs: make(map[string]*gobreaker.CircuitBreaker),
			cfg: cfg,
		}
	})
}

func (s *store) getCB(serviceName string) *gobreaker.CircuitBreaker {
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
	cb = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        fmt.Sprintf("grpc-%s", serviceName),
		MaxRequests: uint32(s.cfg.MaxRequests),
		Interval:    time.Duration(s.cfg.Interval) * time.Second,
		Timeout:     time.Duration(s.cfg.Timeout) * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < uint32(s.cfg.MinRequests) {
				return false
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= s.cfg.FailureRatio
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			pkglog.L().Warn("circuit breaker state changed",
				zap.String("service", name),
				zap.String("from", from.String()),
				zap.String("to", to.String()),
			)
		},
	})
	s.cbs[serviceName] = cb
	return cb
}

// extractServiceName 从 gRPC 方法名解析服务名
func extractServiceName(method string) string {
	for i, c := range method {
		if c == '/' {
			method = method[i+1:]
			break
		}
	}
	for i, c := range method {
		if c == '.' {
			return method[:i]
		}
	}
	return method
}

// UnaryInterceptor 返回按服务隔离的 gRPC 一元拦截器
func UnaryInterceptor(cfg *Config) grpc.UnaryClientInterceptor {
	Init(cfg)

	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		serviceName := extractServiceName(method)
		cb := globalCB.getCB(serviceName)
		_, err := cb.Execute(func() (interface{}, error) {
			return nil, invoker(ctx, method, req, reply, cc, opts...)
		})
		return err
	}
}
