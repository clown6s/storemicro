package dbutil

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	pkgretry "github.com/storemicro/pkg/retry"
	"go.uber.org/zap"
)

// DefaultPoolConfig 默认数据库连接池配置
type DefaultPoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime int // 秒
}

// DefaultPool 返回默认连接池配置
func DefaultPool() DefaultPoolConfig {
	return DefaultPoolConfig{
		MaxOpenConns:    100,
		MaxIdleConns:    25,
		ConnMaxLifetime: 3600, // 1 小时
	}
}

// Connect 创建并验证 MySQL 连接（带重试和连接池配置）
func Connect(driver, dsn string, pool DefaultPoolConfig) (*sql.DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database failed: %w", err)
	}

	// 应用连接池配置
	if pool.MaxOpenConns <= 0 {
		pool.MaxOpenConns = 100
	}
	if pool.MaxIdleConns <= 0 {
		pool.MaxIdleConns = 25
	}
	if pool.ConnMaxLifetime <= 0 {
		pool.ConnMaxLifetime = 3600
	}

	db.SetMaxOpenConns(pool.MaxOpenConns)
	db.SetMaxIdleConns(pool.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(pool.ConnMaxLifetime) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(pool.ConnMaxLifetime/2) * time.Second) // 空闲连接最大存活时间为最大生命周期的半

	// [C5] 带可取消 context 的 MySQL 连接验证
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := pkgretry.Do(ctx, pkgretry.DefaultConfig(), "mysql",
		func(rCtx context.Context) error {
			return db.PingContext(rCtx)
		},
	); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect database failed after retries: %w", err)
	}

	zap.L().Info("database connected",
		zap.Int("max_open_conns", pool.MaxOpenConns),
		zap.Int("max_idle_conns", pool.MaxIdleConns),
	)

	return db, nil
}
