package main

import (
	"context"

	"github.com/sony/gobreaker"
	"google.golang.org/grpc"
)

// circuitBreakerSettings 熔断器配置（SRE 风格：失败率超过 50% 则熔断）
var cbSettings = gobreaker.Settings{
	Name:        "grpc-client",
	MaxRequests: 5,        // 半开状态下允许的最大并发请求数
	Interval:    10 * 1e9, // 统计周期（10秒）
	Timeout:     30 * 1e9, // 熔断开启后的熔断时长（30秒）
	ReadyToTrip: func(counts gobreaker.Counts) bool {
		// 失败率 >= 50% 时触发熔断
		if counts.Requests < 10 {
			return false
		}
		failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
		return failureRatio >= 0.5
	},
}

// circuitBreakerUnaryInterceptor 返回基于 gobreaker 的 gRPC 一元拦截器
func circuitBreakerUnaryInterceptor() grpc.UnaryClientInterceptor {
	cb := gobreaker.NewCircuitBreaker(cbSettings)

	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		_, err := cb.Execute(func() (interface{}, error) {
			return nil, invoker(ctx, method, req, reply, cc, opts...)
		})
		return err
	}
}
