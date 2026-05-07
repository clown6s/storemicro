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

// DistrictRepo 数据仓库接口
type DistrictRepo interface {
	Get(ctx context.Context, adCode string) ([]*District, error)
	List(ctx context.Context, adCode string) ([]*District, error)
}

// DistrictUseCase 行政区用例
type DistrictUseCase struct {
	repo DistrictRepo
}

// NewDistrictUseCase 创建行政区用例
func NewDistrictUseCase(repo DistrictRepo) *DistrictUseCase {
	return &DistrictUseCase{repo: repo}
}

func (u *DistrictUseCase) Get(ctx context.Context, adCode string) ([]*District, error) {
	return u.repo.Get(ctx, adCode)
}
