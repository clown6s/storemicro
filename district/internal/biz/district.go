package biz

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"district/internal/cache"

	"github.com/google/wire"
	"go.uber.org/zap"
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
	repo  DistrictRepo
	cache *cache.Cache
}

// NewDistrictUseCase 创建行政区用例
func NewDistrictUseCase(repo DistrictRepo, c *cache.Cache) *DistrictUseCase {
	return &DistrictUseCase{repo: repo, cache: c}
}

// cacheKey 根据请求参数生成缓存 key
func cacheKey(adCode string) string {
	if adCode == "" {
		return "district:tree:all"
	}
	return fmt.Sprintf("district:tree:%s", adCode)
}

func (u *DistrictUseCase) Get(ctx context.Context, adCode string) ([]*District, error) {
	if adCode != "" && !adCodeRegex.MatchString(adCode) {
		return nil, ErrInvalidAdCode
	}

	// 尝试读缓存
	key := cacheKey(adCode)
	var cached []*District
	if u.cache.Get(key, &cached) {
		zap.L().Debug("district cache hit", zap.String("key", key))
		return cached, nil
	}

	// 缓存未命中，查 DB
	districts, err := u.repo.Get(ctx, adCode)
	if err != nil {
		return nil, err
	}

	// 空结果也缓存（穿透保护），TTL 5 分钟
	if len(districts) == 0 {
		u.cache.Set(key, []*District{}, 5*time.Minute)
		return districts, nil
	}

	// 写入缓存，全量 24h，指定 adCode 1h
	ttl := 1 * time.Hour
	if adCode == "" {
		ttl = 24 * time.Hour
	}
	u.cache.Set(key, districts, ttl)

	return districts, nil
}

// Warmup 预热缓存：启动时加载全量省市区数据
func (u *DistrictUseCase) Warmup(ctx context.Context) {
	zap.L().Info("warming up district cache...")
	districts, err := u.repo.Get(ctx, "")
	if err != nil {
		zap.L().Error("district cache warmup failed", zap.Error(err))
		return
	}
	key := cacheKey("")
	u.cache.Set(key, districts, 24*time.Hour)
	zap.L().Info("district cache warmup completed", zap.Int("count", len(districts)))
}
