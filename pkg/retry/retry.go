package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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
	// OnRetry 每次重试前的回调（用于资源清理等）
	OnRetry func(attempt int, err error)
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

// Do 执行带指数退避 + 随机抖动的重试逻辑。
// fn 接收 context，可在内部使用 ctx 做超时/取消控制。
// fn 应该在成功时返回 nil，在需要重试时返回 error。
// 超过 MaxAttempts 后返回最后一次的 error。
func Do(ctx context.Context, cfg Config, name string, fn func(ctx context.Context) error) error {
	// [L6] MaxAttempts 校验
	if cfg.MaxAttempts < 1 {
		return fmt.Errorf("%s: MaxAttempts must be >= 1, got %d", name, cfg.MaxAttempts)
	}
	// BackoffMultiplier 溢出保护
	if cfg.BackoffMultiplier <= 0 {
		cfg.BackoffMultiplier = 1.0
	}
	if cfg.BackoffMultiplier > 10 {
		cfg.BackoffMultiplier = 10.0
	}

	var lastErr error
	backoff := cfg.InitialBackoff

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		lastErr = fn(ctx)
		if lastErr == nil {
			if attempt > 1 {
				zap.L().Info("retry success",
					zap.String("component", name),
					zap.Int("attempt", attempt),
				)
			}
			return nil
		}

		// 如果错误本身是 context 取消，立即停止重试
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return fmt.Errorf("%s: context error (not retrying): %w", name, lastErr)
		}

		// fn 返回后立即检查 context 取消，避免无意义的等待
		if ctx.Err() != nil {
			return fmt.Errorf("%s: context cancelled after attempt %d: %w", name, attempt, ctx.Err())
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

		// 如果设置了 OnRetry 回调，在等待前调用（用于清理上一次的资源）
		if cfg.OnRetry != nil {
			cfg.OnRetry(attempt, lastErr)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("%s: context cancelled during retry: %w", name, ctx.Err())
		case <-timer.C:
		}

		// [L5] 指数退避 + 等抖动（防止惊群效应，保证退避不低于新基准值的一半）
		newBackoff := time.Duration(float64(backoff) * cfg.BackoffMultiplier)
		if newBackoff <= 0 || newBackoff > cfg.MaxBackoff {
			newBackoff = cfg.MaxBackoff
		}
		// Equal Jitter: [0.5 * newBackoff, newBackoff)
		half := newBackoff / 2
		jitter := time.Duration(rand.Float64() * float64(half))
		backoff = half + jitter
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}

	return fmt.Errorf("%s: failed after %d attempts: %w", name, cfg.MaxAttempts, lastErr)
}
