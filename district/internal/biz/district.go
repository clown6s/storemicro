package biz

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

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
	repo         DistrictRepo
	allDistricts atomic.Value // []*District — 全量省市区数据（atomic.Value 确定性缓存，零 miss）
	subCache     sync.Map     // map[string][]*District — 指定 adCode 子树缓存
	refreshMu    sync.Mutex
	refreshTimer *time.Timer
	closed       atomic.Bool
}

// NewDistrictUseCase 创建行政区用例
func NewDistrictUseCase(repo DistrictRepo) *DistrictUseCase {
	uc := &DistrictUseCase{repo: repo}
	uc.allDistricts.Store([]*District{})
	return uc
}

func (u *DistrictUseCase) Get(ctx context.Context, adCode string) ([]*District, error) {
	if adCode != "" && !adCodeRegex.MatchString(adCode) {
		return nil, ErrInvalidAdCode
	}

	// 全量查询：直接读 atomic.Value（零 miss）
	if adCode == "" {
		return u.allDistricts.Load().([]*District), nil
	}

	// 指定 adCode：先查 sync.Map
	if val, ok := u.subCache.Load(adCode); ok {
		return val.([]*District), nil
	}

	// 缓存未命中，查 DB
	districts, err := u.repo.Get(ctx, adCode)
	if err != nil {
		return nil, err
	}

	// 写入 sync.Map
	u.subCache.Store(adCode, districts)
	return districts, nil
}

// Warmup 预热缓存：启动时加载全量省市区数据到 atomic.Value
func (u *DistrictUseCase) Warmup(ctx context.Context) {
	zap.L().Info("warming up district cache...")
	districts, err := u.repo.Get(ctx, "")
	if err != nil {
		zap.L().Error("district cache warmup failed", zap.Error(err))
		return
	}
	if districts == nil {
		districts = []*District{}
	}
	u.allDistricts.Store(districts)
	zap.L().Info("district cache warmup completed", zap.Int("count", len(districts)))
}

// Refresh 全量刷新缓存（数据变更后调用，防抖 500ms）
func (u *DistrictUseCase) Refresh() {
	u.refreshMu.Lock()
	defer u.refreshMu.Unlock()

	if u.closed.Load() {
		return
	}
	if u.refreshTimer != nil {
		u.refreshTimer.Stop()
	}
	u.refreshTimer = time.AfterFunc(500*time.Millisecond, func() {
		if u.closed.Load() {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			u.Warmup(ctx)
			// 清空子树缓存，下次请求重新查
			u.subCache.Range(func(key, _ any) bool {
				u.subCache.Delete(key)
				return true
			})
		}()
	})
}

// Close 停止刷新定时器
func (u *DistrictUseCase) Close() {
	u.closed.Store(true)
	u.refreshMu.Lock()
	defer u.refreshMu.Unlock()
	if u.refreshTimer != nil {
		u.refreshTimer.Stop()
	}
}
