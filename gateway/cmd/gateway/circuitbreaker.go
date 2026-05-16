package main

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

// cbStore 按服务名隔离的熔断器存储
type cbStore struct {
	mu  sync.RWMutex
	cbs map[string]*gobreaker.CircuitBreaker
	cfg *CircuitBreakerConfig
}

var (
	globalCB   *cbStore
	initCBOnce sync.Once // [I5] 防止重复初始化
)

// initCBStore 初始化熔断器存储（应用启动时调用一次）
func initCBStore(cfg *CircuitBreakerConfig) {
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
		globalCB = &cbStore{
			cbs: make(map[string]*gobreaker.CircuitBreaker),
			cfg: cfg,
		}
	})
}

// getCB 获取指定服务的熔断器（不存在则创建）
func (s *cbStore) getCB(serviceName string) *gobreaker.CircuitBreaker {
	s.mu.RLock()
	cb, ok := s.cbs[serviceName]
	s.mu.RUnlock()
	if ok {
		return cb
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// double check
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
// 方法格式 "/user.User/GetUser" → "user"
func extractServiceName(ctx context.Context, method string) string {
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

// circuitBreakerUnaryInterceptor 返回按服务隔离的 gRPC 一元拦截器
func circuitBreakerUnaryInterceptor(cfg *CircuitBreakerConfig) grpc.UnaryClientInterceptor {
	initCBStore(cfg)

	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		serviceName := extractServiceName(ctx, method)
		cb := globalCB.getCB(serviceName)
		_, err := cb.Execute(func() (interface{}, error) {
			return nil, invoker(ctx, method, req, reply, cc, opts...)
		})
		return err
	}
}
