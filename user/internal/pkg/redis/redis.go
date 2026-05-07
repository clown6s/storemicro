package redis

import (
	"context"
	"fmt"
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
// 同时维护 user_token:{userID} -> token 反向映射，实现单设备登录
func (r *Redis) SetToken(ctx context.Context, token string, userID int64, expire time.Duration) error {
	pipe := r.client.Pipeline()

	// 1) 查旧 token（通过 user_token:{userID} 反向映射）
	oldTokenKey := fmt.Sprintf("storemicro:user_token:%d", userID)
	oldToken, err := r.client.Get(ctx, oldTokenKey).Result()
	if err == nil && oldToken != "" {
		// 2) 删旧 token 的正向映射
		pipe.Del(ctx, "storemicro:token:"+oldToken)
	}

	// 3) 写新 token 正向映射
	pipe.Set(ctx, "storemicro:token:"+token, userID, expire)
	// 4) 写 user_token 反向映射（TTL 与 token 一致）
	pipe.Set(ctx, oldTokenKey, token, expire)

	_, err = pipe.Exec(ctx)
	return err
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
func (r *Redis) DeleteToken(ctx context.Context, token string, userID int64) error {
	pipe := r.client.Pipeline()
	pipe.Del(ctx, "storemicro:token:"+token)
	pipe.Del(ctx, fmt.Sprintf("storemicro:user_token:%d", userID))
	_, err := pipe.Exec(ctx)
	return err
}

// Close 关闭 Redis 连接
func (r *Redis) Close() error {
	return r.client.Close()
}
