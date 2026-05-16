package data

import (
	"database/sql"
	"errors"

	"user/internal/conf"
	"user/internal/pkg/redis"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/wire"
	pkgdb "github.com/storemicro/pkg/dbutil"
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

// NewData 创建 Data 实例（使用共享的 dbutil 连接池和重试逻辑）
func NewData(c *conf.Data, r *redis.Redis) (*Data, func(), error) {
	if c == nil || c.Database == nil {
		return nil, nil, errors.New("data.database config is required")
	}
	if c.Database.Source == "" {
		return nil, nil, errors.New("database source (DSN) is required")
	}
	pool := pkgdb.DefaultPool()
	if c.Database.MaxOpenConns > 0 {
		pool.MaxOpenConns = int(c.Database.MaxOpenConns)
	}
	if c.Database.MaxIdleConns > 0 {
		pool.MaxIdleConns = int(c.Database.MaxIdleConns)
	}
	if c.Database.ConnMaxLifetime > 0 {
		pool.ConnMaxLifetime = int(c.Database.ConnMaxLifetime)
	}
	if c.Database.ConnMaxIdleTime > 0 {
		pool.ConnMaxIdleTime = int(c.Database.ConnMaxIdleTime)
	}

	db, err := pkgdb.Connect("mysql", c.Database.Source, pool)
	if err != nil {
		return nil, nil, err
	}

	d := &Data{db: db, redis: r}
	cleanup := func() {
		zap.L().Info("closing the data resources")
		if err := d.db.Close(); err != nil {
			zap.L().Error("failed to close database", zap.Error(err))
		}
		if d.redis != nil {
			if err := d.redis.Close(); err != nil {
				zap.L().Error("failed to close redis", zap.Error(err))
			}
		}
	}
	return d, cleanup, nil
}
