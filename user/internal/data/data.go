package data

import (
	"context"
	"database/sql"
	"time"

	"user/internal/conf"
	"user/internal/pkg/redis"

	"github.com/go-kratos/kratos/v2/log"
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

// NewData 创建 Data 实例
func NewData(c *conf.Data, r *redis.Redis, logger log.Logger) (*Data, func(), error) {
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

	// 带重试的 MySQL 连接验证（5 次指数退避）
	if err := pkgretry.Do(context.Background(), pkgretry.DefaultConfig(), "mysql",
		func(rCtx context.Context) error {
			return db.PingContext(rCtx)
		},
	); err != nil {
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
