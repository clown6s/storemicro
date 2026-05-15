package data

import (
	"context"
	"database/sql"
	"time"

	"user/internal/conf"
	"user/internal/pkg/redis"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/wire"
	pkgretry "github.com/storemicro/pkg/retry"
	"go.uber.org/zap"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewUserRepo)

// Data 数据库 + Redis 包装
type Data struct {
	db    *sql.DB
	redis *redis.Redis
}

// DB 返回底层数据库连接（供健康检查使用）
func (d *Data) DB() *sql.DB { return d.db }

// Redis 返回底层 Redis 客户端（供健康检查使用）
func (d *Data) Redis() *redis.Redis { return d.redis }

// NewData 创建 Data 实例
func NewData(c *conf.Data, r *redis.Redis) (*Data, func(), error) {
	db, err := sql.Open("mysql", c.Database.Source)
	if err != nil {
		return nil, nil, err
	}

	maxOpenConns := c.Database.MaxOpenConns
	if maxOpenConns <= 0 {
		maxOpenConns = 100
	}
	maxIdleConns := c.Database.MaxIdleConns
	if maxIdleConns <= 0 {
		maxIdleConns = 25
	}
	connMaxLifetime := time.Duration(c.Database.ConnMaxLifetime) * time.Second
	if connMaxLifetime <= 0 {
		connMaxLifetime = time.Hour
	}

	db.SetMaxOpenConns(int(maxOpenConns))
	db.SetMaxIdleConns(int(maxIdleConns))
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(connMaxLifetime / 2) // 空闲连接最大存活时间为最大生命周期的二分之一

	// 带重试的 MySQL 连接验证（5 次指数退避）
	if err := pkgretry.Do(context.Background(), pkgretry.DefaultConfig(), "mysql",
		func(rCtx context.Context) error {
			return db.PingContext(rCtx)
		},
	); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	d := &Data{db: db, redis: r}
	cleanup := func() {
		zap.L().Info("closing the data resources")
		_ = d.db.Close()
		_ = d.redis.Close()
	}
	return d, cleanup, nil
}
