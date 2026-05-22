package cache

import (
	"encoding/json"
	"time"

	"github.com/dgraph-io/ristretto"
	"go.uber.org/zap"
)

const (
	// 全量省市区树缓存 key
	keyAll = "district:tree:all"

	// 默认缓存 TTL：24 小时（行政区划数据极稳定，一天更新一次足够）
	defaultTTL = 24 * time.Hour
)

// Cache 基于 ristretto 的内存级 LRU 缓存
type Cache struct {
	cc *ristretto.Cache
}

// New 创建内存缓存实例
// NumCounters: 估算缓存条目数 x 10，用于 LFU 采样精度
// MaxCost: 缓存最大字节开销（256MB），全量省市区 JSON 约 10MB，留足余量
func New() (*Cache, error) {
	cc, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 100_000, // 预估最多 10000 条缓存
		MaxCost:     1 << 28, // 256MB
		BufferItems: 64,      // 每个获取通道缓冲条数
	})
	if err != nil {
		zap.L().Error("failed to create ristretto cache", zap.Error(err))
		return nil, err
	}
	zap.L().Info("ristretto cache initialized",
		zap.Int("numCounters", 100_000),
		zap.Int64("maxCostMB", 256),
	)
	return &Cache{cc: cc}, nil
}

// Set 存入缓存（value 必须为可 JSON 序列化的 Go 值）
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	data, err := json.Marshal(value)
	if err != nil {
		zap.L().Error("cache marshal failed", zap.String("key", key), zap.Error(err))
		return
	}
	c.cc.SetWithTTL(key, data, int64(len(data)), ttl)
}

// Get 从缓存读取，result 必须为指针类型
func (c *Cache) Get(key string, result any) bool {
	val, ok := c.cc.Get(key)
	if !ok {
		return false
	}
	data, ok := val.([]byte)
	if !ok {
		return false
	}
	if err := json.Unmarshal(data, result); err != nil {
		zap.L().Error("cache unmarshal failed", zap.String("key", key), zap.Error(err))
		return false
	}
	return true
}

// Delete 删除缓存条目
func (c *Cache) Delete(key string) {
	c.cc.Del(key)
}

// Close 关闭缓存（释放内部 goroutine 资源）
func (c *Cache) Close() {
	c.cc.Close()
}
