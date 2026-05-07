package data

import (
	"context"
	"database/sql"
	"time"

	"product/internal/conf"

	"github.com/go-kratos/kratos/v2/log"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/wire"
	"go.uber.org/zap"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewProductRepo)

// Data 数据库包装
type Data struct {
	db *sql.DB
}

// NewData 创建 Data 实例（logger 仅用于 kratos 框架桥接）
func NewData(c *conf.Data, logger log.Logger) (*Data, func(), error) {
	db, err := sql.Open("mysql", c.Database.Source)
	if err != nil {
		return nil, nil, err
	}

	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(time.Hour)

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
