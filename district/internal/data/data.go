package data

import (
	"context"
	"database/sql"
	"time"

	"district/internal/biz"
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

// DistrictRepoImpl 实现 biz.DistrictRepo 接口
type DistrictRepoImpl struct {
	data *Data
}

// NewDistrictRepo 创建 DistrictRepo 实现
func NewDistrictRepo(data *Data) biz.DistrictRepo {
	return &DistrictRepoImpl{data: data}
}

// Get 实现 DistrictRepo.Get 接口
func (r *DistrictRepoImpl) Get(ctx context.Context) (*biz.District, error) {
	row := r.data.db.QueryRowContext(ctx,
		"SELECT id, name, description, price, stock FROM products LIMIT 1",
	)
	var d biz.District
	err := row.Scan(&d.ID, &d.Name, &d.Description, &d.Price, &d.Stock)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
