package retry

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Config 重试配置
type Config struct {
	// MaxAttempts 最大尝试次数（含首次）
	MaxAttempts int
	// InitialBackoff 初始退避时间
	InitialBackoff time.Duration
	// MaxBackoff 最大退避时间
	MaxBackoff time.Duration
	// BackoffMultiplier 退避倍数
	BackoffMultiplier float64
}

// DefaultConfig 返回默认重试配置（5 次，初始 1s，最大 30s，2x 倍增）
func DefaultConfig() Config {
	return Config{
		MaxAttempts:       5,
		InitialBackoff:    1 * time.Second,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// Do 执行带指数退避的重试逻辑。
// fn 应该在成功时返回 nil，在需要重试时返回 error。
// 超过 MaxAttempts 后返回最后一次的 error。
func Do(ctx context.Context, cfg Config, name string, fn func() error) error {
	var lastErr error
	backoff := cfg.InitialBackoff

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			if attempt > 1 {
				zap.L().Info("retry success",
					zap.String("component", name),
					zap.Int("attempt", attempt),
				)
			}
			return nil
		}

		// 最后一次不需要等
		if attempt == cfg.MaxAttempts {
			break
		}

		zap.L().Warn("retrying",
			zap.String("component", name),
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", cfg.MaxAttempts),
			zap.Duration("backoff", backoff),
			zap.Error(lastErr),
		)

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: context cancelled during retry: %w", name, ctx.Err())
		case <-time.After(backoff):
		}

		backoff = time.Duration(float64(backoff) * cfg.BackoffMultiplier)
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}

	return fmt.Errorf("%s: failed after %d attempts: %w", name, cfg.MaxAttempts, lastErr)
}
