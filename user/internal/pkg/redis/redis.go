package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 客户端封装
type Redis struct {
	client *redis.Client
}

// NewRedis 创建 Redis 客户端
func NewRedis(addr, password string, db int) *Redis {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	// 验证连接
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		panic("failed to connect redis: " + err.Error())
	}
	return &Redis{client: client}
}

// SetToken 存储 token（登录成功后将 token -> userID 写入 Redis，设置过期时间）
func (r *Redis) SetToken(ctx context.Context, token string, userID int64, expire time.Duration) error {
	return r.client.Set(ctx, "storemicro:token:"+token, userID, expire).Err()
}

// GetUserIDByToken 通过 token 查询 userID，同时起到"验证 token 是否有效"的作用
func (r *Redis) GetUserIDByToken(ctx context.Context, token string) (int64, error) {
	result := r.client.Get(ctx, "storemicro:token:"+token)
	if err := result.Err(); err != nil {
		return 0, err
	}
	var userID int64
	if err := result.Scan(&userID); err != nil {
		return 0, err
	}
	return userID, nil
}

// DeleteToken 删除 token（登出时使用）
func (r *Redis) DeleteToken(ctx context.Context, token string) error {
	return r.client.Del(ctx, "token:"+token).Err()
}

// Close 关闭 Redis 连接
func (r *Redis) Close() error {
	return r.client.Close()
}
