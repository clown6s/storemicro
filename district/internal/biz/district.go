package biz

import (
	"context"

	"github.com/google/wire"
)

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewDistrictUseCase)

// District 领域模型
type District struct {
	ID          int64
	Name        string
	Description string
	Price       int64 // 分
	Stock       int32
}

// DistrictUseCase 行政区用例
type DistrictUseCase struct {
	repo DistrictRepo
}

// DistrictRepo 数据仓库接口
type DistrictRepo interface {
	Get(ctx context.Context) (*District, error)
}

// NewDistrictUseCase 创建行政区用例
func NewDistrictUseCase(repo DistrictRepo) *DistrictUseCase {
	return &DistrictUseCase{repo: repo}
}
