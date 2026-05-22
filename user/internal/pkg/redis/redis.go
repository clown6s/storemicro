package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	pkgretry "github.com/storemicro/pkg/retry"
)

// Redis key 子前缀（不含环境隔离前缀，与 keyPrefix 拼接使用）
const (
	tokenKeySuffix            = "user:token:"
	userTokenMapSuffix        = "user:token_map:"
	refreshTokenKeySuffix     = "user:refresh_token:"
	userRefreshTokenMapSuffix = "user:refresh_token_map:"
	registerPhoneLimitSuffix  = "register:phone_limit:"
)

// defaultKeyPrefix 默认 key 前缀（多环境共享 Redis 时通过 NewRedis 的 keyPrefix 参数覆盖）
const defaultKeyPrefix = "storemicro:"

// hashToken 计算 token 的 SHA-256 哈希，用于 Redis key 存储（防止 Redis 被攻破泄露原始 token）
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Redis 客户端封装
type Redis struct {
	client    *redis.Client
	keyPrefix string
}

// NewRedis 创建 Redis 客户端（带连接池、超时、重试配置）
// 连接池参数从 conf.proto 读取，传入 0 时使用默认值
// keyPrefix 为 Redis key 环境隔离前缀，空字符串时使用默认值 "storemicro:"
func NewRedis(addr, password string, db int, poolSize, minIdleConns int32, dialTimeout, readTimeout, writeTimeout time.Duration, maxRetries int32, keyPrefix string) (*Redis, error) {
	// 默认值兜底
	if poolSize <= 0 {
		poolSize = 20
	}
	if minIdleConns <= 0 {
		minIdleConns = 5
	}
	if dialTimeout <= 0 {
		dialTimeout = 3 * time.Second
	}
	if readTimeout <= 0 {
		readTimeout = 5 * time.Second
	}
	if writeTimeout <= 0 {
		writeTimeout = 5 * time.Second
	}
	if maxRetries <= 0 {
		maxRetries = 3
	}
	if keyPrefix == "" {
		keyPrefix = defaultKeyPrefix
	}

	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		PoolSize:     int(poolSize),
		MinIdleConns: int(minIdleConns),
		DialTimeout:  dialTimeout,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		MaxRetries:   int(maxRetries),
	})
	// 验证连接（带重试，60s 整体超时）
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	return &Redis{client: client, keyPrefix: keyPrefix}, nil
}

// Key 构建辅助方法（使用实例 keyPrefix 实现环境隔离）
func (r *Redis) tokenKey(hash string) string { return r.keyPrefix + tokenKeySuffix + hash }
func (r *Redis) userTokenMapKey(userID int64) string {
	return r.keyPrefix + userTokenMapSuffix + fmt.Sprintf("%d", userID)
}
func (r *Redis) refreshTokenKey(hash string) string {
	return r.keyPrefix + refreshTokenKeySuffix + hash
}
func (r *Redis) userRefreshTokenMapKey(userID int64) string {
	return r.keyPrefix + userRefreshTokenMapSuffix + fmt.Sprintf("%d", userID)
}
func (r *Redis) registerPhoneLimitKey(phone string) string {
	return r.keyPrefix + registerPhoneLimitSuffix + phone
}

// setTokenScript Lua 脚本原子化 token 操作，解决并发登录竞态条件
// KEYS[1] = token正向映射 key (sha256), KEYS[2] = user_token反向映射 key
// ARGV[1] = sha256(新token), ARGV[2] = userID, ARGV[3] = 过期秒数, ARGV[4] = token key 前缀
//
// 流程：读取旧hash → 删除旧正向映射 → 写新正向映射 → 写反向映射（存 hash）
// 所有操作在 Lua 中原子执行，避免并发登录时 token 被偷
var setTokenScript = redis.NewScript(`
local prefix = ARGV[4]
local oldHash = redis.call('GET', KEYS[2])
if oldHash and oldHash ~= '' then
	redis.call('DEL', prefix .. oldHash)
end
redis.call('SET', KEYS[1], ARGV[2], 'EX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[1], 'EX', ARGV[3])
return 1
`)

// deleteTokenScript Lua 脚本原子化 token 删除操作
// KEYS[1] = token正向映射 key (sha256), KEYS[2] = user_token反向映射 key
// ARGV[1] = sha256(当前token)（用于 CAS 校验，防止并发 Login 写入新 token 后被误删）
// 只有反向映射中的 hash 与当前 hash 一致时才删除，避免竞态条件
var deleteTokenScript = redis.NewScript(`
local current = redis.call('GET', KEYS[2])
if current ~= ARGV[1] then
	-- 反向映射已被并发 Login 更新为其他 hash，不删除
	return 0
end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
return 1
`)

// invalidateTokenScript Lua 脚本原子化 token 失效操作（用户删除/禁用时使用）
// KEYS[1] = user_token反向映射 key
// ARGV[1] = token key 前缀
// 流程：读取当前hash → 删除正向映射 → 删除反向映射
var invalidateTokenScript = redis.NewScript(`
local prefix = ARGV[1]
local tokenHash = redis.call('GET', KEYS[1])
if tokenHash and tokenHash ~= '' then
	redis.call('DEL', prefix .. tokenHash)
end
redis.call('DEL', KEYS[1])
return 1
`)

// SetToken 存储 token（登录成功后将 sha256(token) -> userID 写入 Redis，设置过期时间）
// 反向映射 user_token_map:{userID} -> sha256(token)，实现单设备登录
// 使用 Lua 脚本保证原子性，避免并发登录竞态条件
func (r *Redis) SetToken(ctx context.Context, token string, userID int64, expire time.Duration) error {
	tokenHash := hashToken(token)
	tokenKey := r.tokenKey(tokenHash)
	userTokenKey := r.userTokenMapKey(userID)
	expireSec := int64(expire.Seconds())

	_, err := setTokenScript.Run(ctx, r.client, []string{tokenKey, userTokenKey}, tokenHash, userID, expireSec, r.keyPrefix+tokenKeySuffix).Result()
	return err
}

// GetUserIDByToken 通过 token 查询 userID（使用 SHA-256 哈希作为 key），同时起到"验证 token 是否有效"的作用
func (r *Redis) GetUserIDByToken(ctx context.Context, token string) (int64, error) {
	result := r.client.Get(ctx, r.tokenKey(hashToken(token)))
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
// #3: 使用 SHA-256 哈希作为 key，反向映射也存 hash
func (r *Redis) DeleteToken(ctx context.Context, token string, userID int64) error {
	tokenHash := hashToken(token)
	tokenKey := r.tokenKey(tokenHash)
	userTokenKey := r.userTokenMapKey(userID)

	_, err := deleteTokenScript.Run(ctx, r.client, []string{tokenKey, userTokenKey}, tokenHash).Result()
	return err
}

// Ping 检查 Redis 连接（供健康检查使用）
func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// InvalidateUserTokens 删除用户所有活跃 token（用户删除/禁用时使用）
// 通过反向映射 user_token_map:{userID} 找到当前 token hash，然后原子删除双向映射
func (r *Redis) InvalidateUserTokens(ctx context.Context, userID int64) error {
	userTokenKey := r.userTokenMapKey(userID)

	_, err := invalidateTokenScript.Run(ctx, r.client, []string{userTokenKey}, r.keyPrefix+tokenKeySuffix).Result()
	return err
}

// SetRefreshToken 存储 refresh token（原子操作：正向映射 + 反向映射）
// 正向: refresh_token:{hash(token)} -> userID
// 反向: refresh_token_map:{userID} -> hash(token)，实现单设备 refresh token
func (r *Redis) SetRefreshToken(ctx context.Context, token string, userID int64, expireHours int64) error {
	tokenHash := hashToken(token)
	refreshTokenKey := r.refreshTokenKey(tokenHash)
	userRefreshTokenKey := r.userRefreshTokenMapKey(userID)
	expireSec := int64(time.Duration(expireHours) * time.Hour / time.Second)

	_, err := setRefreshTokenScript.Run(ctx, r.client,
		[]string{refreshTokenKey, userRefreshTokenKey},
		tokenHash, userID, expireSec, r.keyPrefix+refreshTokenKeySuffix,
	).Result()
	return err
}

// GetRefreshTokenUserID 通过 refresh token 查询 userID
func (r *Redis) GetRefreshTokenUserID(ctx context.Context, token string) (int64, error) {
	result := r.client.Get(ctx, r.refreshTokenKey(hashToken(token)))
	if err := result.Err(); err != nil {
		return 0, err
	}
	var userID int64
	if err := result.Scan(&userID); err != nil {
		return 0, err
	}
	return userID, nil
}

// DeleteRefreshToken 删除 refresh token（CAS 原子操作，防止并发竞态）
func (r *Redis) DeleteRefreshToken(ctx context.Context, token string, userID int64) error {
	tokenHash := hashToken(token)
	refreshTokenKey := r.refreshTokenKey(tokenHash)
	userRefreshTokenKey := r.userRefreshTokenMapKey(userID)

	_, err := deleteRefreshTokenScript.Run(ctx, r.client,
		[]string{refreshTokenKey, userRefreshTokenKey},
		tokenHash,
	).Result()
	return err
}

// InvalidateUserRefreshTokens 删除用户所有活跃的 refresh token（logout/改密码时使用）
func (r *Redis) InvalidateUserRefreshTokens(ctx context.Context, userID int64) error {
	userRefreshTokenKey := r.userRefreshTokenMapKey(userID)

	_, err := invalidateRefreshTokenScript.Run(ctx, r.client,
		[]string{userRefreshTokenKey}, r.keyPrefix+refreshTokenKeySuffix,
	).Result()
	return err
}

// CheckRegisterPhoneRateLimit 检查手机号注册频率（5分钟内同一手机号只能注册一次）
// Redis 故障时 fail-open，不影响注册
func (r *Redis) CheckRegisterPhoneRateLimit(ctx context.Context, phone string) error {
	key := r.registerPhoneLimitKey(phone)
	ok, err := r.client.SetNX(ctx, key, "1", 5*time.Minute).Result()
	if err != nil {
		return nil // fail-open
	}
	if !ok {
		return errors.New("too many registration attempts for this phone number")
	}
	return nil
}

// Close 关闭 Redis 连接
func (r *Redis) Close() error {
	return r.client.Close()
}

// setRefreshTokenScript Lua 脚本原子化 refresh token 存储操作
// 与 access token 的 setTokenScript 逻辑一致：读取旧 hash → 删旧正向 → 写新正向 → 写反向
var setRefreshTokenScript = redis.NewScript(`
local prefix = ARGV[4]
local oldHash = redis.call('GET', KEYS[2])
if oldHash and oldHash ~= '' then
	redis.call('DEL', prefix .. oldHash)
end
redis.call('SET', KEYS[1], ARGV[2], 'EX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[1], 'EX', ARGV[3])
return 1
`)

// deleteRefreshTokenScript Lua 脚本原子化 refresh token 删除操作
// 与 access token 的 deleteTokenScript 逻辑一致：CAS 校验反向映射
var deleteRefreshTokenScript = redis.NewScript(`
local current = redis.call('GET', KEYS[2])
if current ~= ARGV[1] then
	return 0
end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
return 1
`)

// invalidateRefreshTokenScript Lua 脚本原子化 refresh token 失效操作
// 通过反向映射找到当前 refresh token hash 并删除双向映射
var invalidateRefreshTokenScript = redis.NewScript(`
local prefix = ARGV[1]
local tokenHash = redis.call('GET', KEYS[1])
if tokenHash and tokenHash ~= '' then
	redis.call('DEL', prefix .. tokenHash)
end
redis.call('DEL', KEYS[1])
return 1
`)

// loginTokenScript Lua 脚本原子化 Login 操作（access + refresh token 一次性写入）
// KEYS[1]=access正向, KEYS[2]=access反向, KEYS[3]=refresh正向, KEYS[4]=refresh反向
// ARGV[1]=sha256(access), ARGV[2]=sha256(refresh), ARGV[3]=userID
// ARGV[4]=access过期秒数, ARGV[5]=refresh过期秒数
// ARGV[6]=access key前缀, ARGV[7]=refresh key前缀
var loginTokenScript = redis.NewScript(`
local oldAccessHash = redis.call('GET', KEYS[2])
if oldAccessHash and oldAccessHash ~= '' then
	redis.call('DEL', ARGV[6] .. oldAccessHash)
end
local oldRefreshHash = redis.call('GET', KEYS[4])
if oldRefreshHash and oldRefreshHash ~= '' then
	redis.call('DEL', ARGV[7] .. oldRefreshHash)
end
redis.call('SET', KEYS[1], ARGV[3], 'EX', ARGV[4])
redis.call('SET', KEYS[2], ARGV[1], 'EX', ARGV[4])
redis.call('SET', KEYS[3], ARGV[3], 'EX', ARGV[5])
redis.call('SET', KEYS[4], ARGV[2], 'EX', ARGV[5])
return 1
`)

// rotateRefreshTokenScript Lua 脚本原子化 refresh token 旋转操作（删旧 + 写新）
// KEYS[1]=旧refresh正向, KEYS[2]=refresh反向, KEYS[3]=新refresh正向
// ARGV[1]=sha256(旧token), ARGV[2]=sha256(新token), ARGV[3]=userID
// ARGV[4]=过期秒数, ARGV[5]=refresh key前缀
var rotateRefreshTokenScript = redis.NewScript(`
local current = redis.call('GET', KEYS[2])
if current ~= ARGV[1] then
	return 0
end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
redis.call('SET', KEYS[3], ARGV[3], 'EX', ARGV[4])
redis.call('SET', KEYS[2], ARGV[2], 'EX', ARGV[4])
return 1
`)

// LoginStoreTokens 原子存储 access token + refresh token（Login 使用）
// 单次 Lua 调用完成：删除旧映射 + 写入新映射，保证原子性
func (r *Redis) LoginStoreTokens(ctx context.Context, accessToken, refreshToken string, userID int64, accessExpireHours, refreshExpireHours int64) error {
	accessHash := hashToken(accessToken)
	refreshHash := hashToken(refreshToken)
	accessExpireSec := int64(time.Duration(accessExpireHours) * time.Hour / time.Second)
	refreshExpireSec := int64(time.Duration(refreshExpireHours) * time.Hour / time.Second)

	_, err := loginTokenScript.Run(ctx, r.client,
		[]string{
			r.tokenKey(accessHash), r.userTokenMapKey(userID),
			r.refreshTokenKey(refreshHash), r.userRefreshTokenMapKey(userID),
		},
		accessHash, refreshHash, userID,
		accessExpireSec, refreshExpireSec,
		r.keyPrefix+tokenKeySuffix, r.keyPrefix+refreshTokenKeySuffix,
	).Result()
	return err
}

// RotateRefreshToken 原子旋转 refresh token（CAS 校验 + 删旧 + 写新）
// 防止并发 refresh 时状态不一致（删旧成功但写新失败导致用户丢失 refresh token）
func (r *Redis) RotateRefreshToken(ctx context.Context, oldRefreshToken, newRefreshToken string, userID int64, expireHours int64) error {
	oldHash := hashToken(oldRefreshToken)
	newHash := hashToken(newRefreshToken)
	expireSec := int64(time.Duration(expireHours) * time.Hour / time.Second)

	_, err := rotateRefreshTokenScript.Run(ctx, r.client,
		[]string{
			r.refreshTokenKey(oldHash),
			r.userRefreshTokenMapKey(userID),
			r.refreshTokenKey(newHash),
		},
		oldHash, newHash, userID,
		expireSec, r.keyPrefix+refreshTokenKeySuffix,
	).Result()
	return err
}
