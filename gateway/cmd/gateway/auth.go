package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// corsMiddleware CORS 跨域中间件（白名单模式，防止任意 Origin 窃取凭证）
func corsMiddleware(allowOrigins []string) func(http.Handler) http.Handler {
	originSet := make(map[string]struct{}, len(allowOrigins))
	for _, o := range allowOrigins {
		originSet[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if _, ok := originSet[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Set("Access-Control-Max-Age", "86400")
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Claims JWT claims，与 user 服务保持一致
type Claims struct {
	UserID int64  `json:"user_id"`
	Phone  string `json:"phone"`
	jwt.RegisteredClaims
}

// contextKey 自定义类型用于 context key，避免与其他包的 key 碰撞
type contextKey string

const (
	ContextKeyUserID contextKey = "user_id"
	ContextKeyPhone  contextKey = "phone"
)

// noAuthPrefixes 不需要 JWT 校验的路径前缀（登录、注册、健康检查等）
var noAuthPrefixes = []string{
	"/api/v1/user/register",
	"/api/v1/user/login",
	"/api/v1/user/ping",
	"/api/v1/product/ping",
	"/healthz",
}

// redisRateLimiter 基于 Redis ZSET 滑动窗口限流（支持多实例共享）
type redisRateLimiter struct {
	rdb         *redis.Client
	maxRequests int64
	windowSec   int64
}

func newRedisRateLimiter(rdb *redis.Client, max, window int) *redisRateLimiter {
	return &redisRateLimiter{
		rdb:         rdb,
		maxRequests: int64(max),
		windowSec:   int64(window),
	}
}

// allow 使用 Redis ZSET 滑动窗口检查是否放行
func (rl *redisRateLimiter) allow(ctx context.Context, key string) bool {
	now := time.Now().UnixMilli()
	windowStart := now - rl.windowSec*1000

	pipe := rl.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStart, 10))
	countCmd := pipe.ZCard(ctx, key)
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
	pipe.Expire(ctx, key, time.Duration(rl.windowSec)*time.Second)
	_, err := pipe.Exec(ctx)

	if err != nil {
		// Redis 异常时降级放行
		return true
	}
	return countCmd.Val() < rl.maxRequests
}

// jwtAuth JWT 认证中间件（JWT 签名校验 + Redis token 有效性校验，支持单设备登录踢人）
func jwtAuth(secret string, rdb *redis.Client, next http.Handler) http.Handler {
	// 基于 Redis 的限流器（多 gateway 实例共享计数）
	rateLimitPaths := map[string]*redisRateLimiter{
		"/api/v1/user/login":    newRedisRateLimiter(rdb, 5, 60), // 5 次/分钟
		"/api/v1/user/register": newRedisRateLimiter(rdb, 3, 60), // 3 次/分钟
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 白名单路径前缀匹配
		allowed := false
		for _, prefix := range noAuthPrefixes {
			if strings.HasPrefix(path, prefix) {
				allowed = true
				break
			}
		}
		if allowed {
			// 限流检查（登录、注册等敏感接口）
			if limiter, ok := rateLimitPaths[path]; ok {
				ip := extractIP(r)
				if !limiter.allow(r.Context(), "storemicro:rl:"+path+":"+ip) {
					zapLog.Warn("rate limited", zap.String("path", path), zap.String("ip", ip))
					writeJSON(w, http.StatusTooManyRequests, `{"code":429,"message":"too many requests"}`)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		// 从 Authorization header 取 token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			zapLog.Warn("jwt auth failed: missing Authorization header",
				zap.String("path", path),
				zap.String("remote", r.RemoteAddr),
			)
			writeJSON(w, http.StatusUnauthorized, `{"code":401,"message":"missing token"}`)
			return
		}

		// 支持 Bearer 前缀（大小写不敏感）
		tokenStr := authHeader
		if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
			tokenStr = strings.TrimSpace(authHeader[7:])
		}

		// 1) 解析并校验 JWT 签名
		token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			zapLog.Warn("jwt auth failed: invalid token",
				zap.String("path", path),
				zap.Error(err),
			)
			writeJSON(w, http.StatusUnauthorized, `{"code":401,"message":"invalid token"}`)
			return
		}

		claims, ok := token.Claims.(*Claims)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, `{"code":401,"message":"invalid claims"}`)
			return
		}

		// 2) Redis 校验：token 是否仍然有效（被踢掉的旧 token 会被删除）
		val, err := rdb.Get(r.Context(), "storemicro:token:"+tokenStr).Result()
		if err == redis.Nil {
			zapLog.Warn("jwt auth failed: token revoked (logged in elsewhere)",
				zap.String("path", path),
				zap.Int64("user_id", claims.UserID),
			)
			writeJSON(w, http.StatusUnauthorized, `{"code":401,"message":"token expired, please re-login"}`)
			return
		} else if err != nil {
			zapLog.Error("redis check token failed", zap.Error(err))
			writeJSON(w, http.StatusServiceUnavailable, `{"code":503,"message":"service temporarily unavailable"}`)
			return
		} else {
			_ = val // val 是 userID，可按需记录
		}

		// 将 user_id / phone 用类型安全的 key 写入 context
		ctx := context.WithValue(r.Context(), ContextKeyUserID, claims.UserID)
		ctx = context.WithValue(ctx, ContextKeyPhone, claims.Phone)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}

// writeJSON 统一写入 JSON 错误响应
func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// extractIP 提取客户端真实 IP（优先 X-Forwarded-For，兼容 X-Real-IP）
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return rip
	}
	// RemoteAddr 格式 "ip:port"
	idx := strings.LastIndex(r.RemoteAddr, ":")
	if idx == -1 {
		return r.RemoteAddr
	}
	return r.RemoteAddr[:idx]
}
