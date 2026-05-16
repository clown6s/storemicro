package biz

import (
	"context"
	"errors"
	"regexp"

	"github.com/google/wire"
)

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewDistrictUseCase)

// adCodeRegex 中国行政区划代码：6 位数字（如 110000）
var adCodeRegex = regexp.MustCompile(`^\d{6}$`)

var ErrInvalidAdCode = errors.New("invalid ad_code format, expected 6 digits")

// District 行政区领域模型
type District struct {
	ID           int64
	ParentAdCode string
	AdCode       string
	Name         string
	Level        string
	Center       string
}

// DistrictRepo 数据仓库接口
type DistrictRepo interface {
	Get(ctx context.Context, adCode string) ([]*District, error)
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
	if adCode != "" && !adCodeRegex.MatchString(adCode) {
		return nil, ErrInvalidAdCode
	}
	return u.repo.Get(ctx, adCode)
}
