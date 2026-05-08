package data

import (
	"context"
	"database/sql"
	"time"

	"district/internal/conf"

	"github.com/go-kratos/kratos/v2/log"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/wire"
	"go.uber.org/zap"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewDistrictRepo)

// Data 数据库包装
type Data struct {
	db *sql.DB
}

// NewData 创建 Data 实例
func NewData(c *conf.Data, logger log.Logger) (*Data, func(), error) {
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

	if err := db.PingContext(context.Background()); err != nil {
		return nil, nil, err
	}

	d := &Data{db: db}
	cleanup := func() {
		zap.L().Info("closing the data resources")
		_ = d.db.Close()
	}
	return d, cleanup, nil
}
