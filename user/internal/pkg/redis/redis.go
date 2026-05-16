package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	pkgretry "github.com/storemicro/pkg/retry"
)

// Redis 客户端封装
type Redis struct {
	client *redis.Client
}

// NewRedis 创建 Redis 客户端（带连接池、超时、重试配置）
// TODO: 连接池参数（PoolSize/MinIdleConns）目前硬编码，后续应通过 conf.proto 配置化
func NewRedis(addr, password string, db int) (*Redis, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		PoolSize:     20,
		MinIdleConns: 5,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		MaxRetries:   3,
	})
	// 验证连接（带重试，30s 整体超时）
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pingCancel()
	if err := pkgretry.Do(pingCtx, pkgretry.DefaultConfig(), "redis",
		func(rCtx context.Context) error {
			ctx, cancel := context.WithTimeout(rCtx, 5*time.Second)
			defer cancel()
			return client.Ping(ctx).Err()
		},
	); err != nil {
		return nil, fmt.Errorf("failed to connect redis after retries: %w", err)
	}
	return &Redis{client: client}, nil
}

// setTokenScript Lua 脚本原子化 token 操作，解决并发登录竞态条件
// KEYS[1] = token正向映射 key, KEYS[2] = user_token反向映射 key
// ARGV[1] = 新token, ARGV[2] = userID, ARGV[3] = 过期秒数
//
// 流程：读取旧token → 删除旧正向映射 → 写新正向映射 → 写反向映射
// 所有操作在 Lua 中原子执行，避免并发登录时 token 被偷
// 注意：Lua 脚本中 storemicro:user:token: 前缀必须与 Go 代码中 tokenKey 前缀保持一致
var setTokenScript = redis.NewScript(`
local oldToken = redis.call('GET', KEYS[2])
if oldToken and oldToken ~= '' then
	redis.call('DEL', 'storemicro:user:token:' .. oldToken)
end
redis.call('SET', KEYS[1], ARGV[2], 'EX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[1], 'EX', ARGV[3])
return 1
`)

// deleteTokenScript Lua 脚本原子化 token 删除操作
// KEYS[1] = token正向映射 key, KEYS[2] = user_token反向映射 key
// ARGV[1] = 当前 token（用于 CAS 校验，防止并发 Login 写入新 token 后被误删）
// 只有反向映射中的 token 与当前 token 一致时才删除，避免竞态条件
var deleteTokenScript = redis.NewScript(`
local current = redis.call('GET', KEYS[2])
if current ~= ARGV[1] then
	-- 反向映射已被并发 Login 更新为其他 token，不删除
	return 0
end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
return 1
`)

// invalidateTokenScript Lua 脚本原子化 token 失效操作（用户删除/禁用时使用）
// KEYS[1] = user_token反向映射 key
// 流程：读取当前token → 删除正向映射 → 删除反向映射
// 注意：Lua 脚本中 storemicro:user:token: 前缀必须与 Go 代码中 tokenKey 前缀保持一致
var invalidateTokenScript = redis.NewScript(`
local token = redis.call('GET', KEYS[1])
if token and token ~= '' then
	redis.call('DEL', 'storemicro:user:token:' .. token)
end
redis.call('DEL', KEYS[1])
return 1
`)

// SetToken 存储 token（登录成功后将 token -> userID 写入 Redis，设置过期时间）
// 同时维护 user:user:token_map:{userID} -> token 反向映射，实现单设备登录
// 使用 Lua 脚本保证原子性，避免并发登录竞态条件
func (r *Redis) SetToken(ctx context.Context, token string, userID int64, expire time.Duration) error {
	tokenKey := fmt.Sprintf("storemicro:user:token:%s", token)
	userTokenKey := fmt.Sprintf("storemicro:user:token_map:%d", userID)
	expireSec := int64(expire.Seconds())

	_, err := setTokenScript.Run(ctx, r.client, []string{tokenKey, userTokenKey}, token, userID, expireSec).Result()
	return err
}

// GetUserIDByToken 通过 token 查询 userID，同时起到"验证 token 是否有效"的作用
func (r *Redis) GetUserIDByToken(ctx context.Context, token string) (int64, error) {
	result := r.client.Get(ctx, "storemicro:user:token:"+token)
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
// [M9] 改用 Lua 脚本保证原子性，与 SetToken 保持一致
func (r *Redis) DeleteToken(ctx context.Context, token string, userID int64) error {
	tokenKey := fmt.Sprintf("storemicro:user:token:%s", token)
	userTokenKey := fmt.Sprintf("storemicro:user:token_map:%d", userID)

	_, err := deleteTokenScript.Run(ctx, r.client, []string{tokenKey, userTokenKey}, token).Result()
	return err
}

// Ping 检查 Redis 连接（供健康检查使用）
func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// InvalidateUserTokens 删除用户所有活跃 token（用户删除/禁用时使用）
// 通过反向映射 user_token:{userID} 找到当前 token，然后原子删除双向映射
func (r *Redis) InvalidateUserTokens(ctx context.Context, userID int64) error {
	userTokenKey := fmt.Sprintf("storemicro:user:token_map:%d", userID)

	_, err := invalidateTokenScript.Run(ctx, r.client, []string{userTokenKey}).Result()
	return err
}

// Close 关闭 Redis 连接
func (r *Redis) Close() error {
	return r.client.Close()
}
