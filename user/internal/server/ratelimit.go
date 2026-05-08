package server

import (
	"github.com/go-kratos/aegis/ratelimit"
	"golang.org/x/time/rate"
)

// tokenLimiter 基于 golang.org/x/time/rate 的 QPS 限流器
type tokenLimiter struct {
	limiter *rate.Limiter
}

func newTokenLimiter(qps int64) *tokenLimiter {
	return &tokenLimiter{limiter: rate.NewLimiter(rate.Limit(qps), int(qps))}
}

// Allow 检查是否允许请求
func (t *tokenLimiter) Allow() (ratelimit.DoneFunc, error) {
	if !t.limiter.Allow() {
		return nil, ratelimit.ErrLimitExceed
	}
	return func(info ratelimit.DoneInfo) {}, nil
}
