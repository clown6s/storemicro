package biz

import (
	"context"

	"github.com/google/wire"
)

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewDistrictUseCase)

// District 行政区领域模型
type District struct {
	ID           int64
	ParentAdCode string
	AdCode       string
	Name         string
	Level        string
	Center       string
	Children     []*District
}

// DistrictUseCase 行政区用例
type DistrictUseCase struct {
	repo DistrictRepo
}

// DistrictRepo 数据仓库接口
type DistrictRepo interface {
	GetTree(ctx context.Context, adCode string) ([]*District, error)
}

// NewDistrictUseCase 创建行政区用例
func NewDistrictUseCase(repo DistrictRepo) *DistrictUseCase {
	return &DistrictUseCase{repo: repo}
}

// GetTree 获取行政区树
func (uc *DistrictUseCase) GetTree(ctx context.Context, adCode string) ([]*District, error) {
	return uc.repo.GetTree(ctx, adCode)
}
