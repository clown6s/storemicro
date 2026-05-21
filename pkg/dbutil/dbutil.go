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
	ConnMaxIdleTime int // 秒，0 表示自动取 ConnMaxLifetime/2
}

// DefaultPool 返回默认连接池配置
func DefaultPool() DefaultPoolConfig {
	return DefaultPoolConfig{
		MaxOpenConns:    100,
		MaxIdleConns:    25,
		ConnMaxLifetime: 3600, // 1 小时
	}
}

// allowedDrivers 数据库驱动白名单
var allowedDrivers = map[string]bool{"mysql": true}

// Connect 创建并验证 MySQL 连接（带重试和连接池配置）
// WARNING: dsn 参数包含数据库密码，严禁在日志或错误信息中输出 dsn 内容
func Connect(driver, dsn string, pool DefaultPoolConfig) (*sql.DB, error) {
	if !allowedDrivers[driver] {
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database failed: %w", err)
	}

	// 应用默认值（使用 DefaultPool 作为基准，覆盖调用方未设置的零值）
	defaults := DefaultPool()
	if pool.MaxOpenConns <= 0 {
		pool.MaxOpenConns = defaults.MaxOpenConns
	}
	if pool.MaxIdleConns <= 0 {
		pool.MaxIdleConns = defaults.MaxIdleConns
	}
	if pool.ConnMaxLifetime <= 0 {
		pool.ConnMaxLifetime = defaults.ConnMaxLifetime
	}

	db.SetMaxOpenConns(pool.MaxOpenConns)
	db.SetMaxIdleConns(pool.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(pool.ConnMaxLifetime) * time.Second)
	// 空闲连接最大存活时间：优先用独立配置，否则取最大生命周期的半，最少 60s
	idleTime := pool.ConnMaxIdleTime
	if idleTime <= 0 {
		idleTime = pool.ConnMaxLifetime / 2
	}
	if idleTime < 60 {
		idleTime = 60
	}
	db.SetConnMaxIdleTime(time.Duration(idleTime) * time.Second)

	// [C5] 带可取消 context 的 MySQL 连接验证
	// 注意：context timeout (60s) 是硬上限，可能在所有重试完成前到期
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
