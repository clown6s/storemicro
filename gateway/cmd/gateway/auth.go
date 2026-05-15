package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	pkglog "github.com/storemicro/pkg/zaplogger"
	"go.uber.org/zap"
)

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

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

type Claims struct {
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

type contextKey string

const (
	ContextKeyUserID contextKey = "user_id"
)

// noAuthRoutes is exact METHOD+path allowlist. Do not use prefix matching here.
var noAuthRoutes = map[string]struct{}{
	"POST /api/v1/user/register": {},
	"POST /api/v1/user/login":    {},
	"GET /healthz":               {},
}

// [H2] rateLimitScript Lua 脚本原子化限流操作
// KEYS[1] = 限流 key
// ARGV[1] = 窗口起始时间（毫秒），ARGV[2] = 当前时间（毫秒），ARGV[3] = 最大请求数，ARGV[4] = 过期秒数
// 流程：清理过期成员 → 统计窗口内请求数 → 添加新请求 → 设置过期
// 全部在 Lua 中原子执行，避免 Pipeline 非原子竞态
var rateLimitScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '0', ARGV[1])
local count = redis.call('ZCARD', KEYS[1])
if tonumber(count) >= tonumber(ARGV[3]) then
    return count
end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[2] .. '-' .. math.random(1000000))
redis.call('EXPIRE', KEYS[1], ARGV[4])
return count + 1
`)

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

func (rl *redisRateLimiter) allow(ctx context.Context, key string) bool {
	now := time.Now().UnixMilli()
	windowStart := now - rl.windowSec*1000

	// [M13] Member 使用 时间戳-随机数 保证唯一性
	result, err := rateLimitScript.Run(ctx, rl.rdb,
		[]string{key},
		strconv.FormatInt(windowStart, 10),
		strconv.FormatInt(now, 10),
		strconv.FormatInt(rl.maxRequests, 10),
		strconv.FormatInt(rl.windowSec, 10),
	).Int64()

	if err != nil {
		pkglog.L().Error("rate limiter redis error, fail-closed",
			zap.String("key", key),
			zap.Error(err),
		)
		return false
	}
	return result <= rl.maxRequests
}

func jwtAuth(secret string, rdb *redis.Client, trustedProxies []string, next http.Handler) http.Handler {
	rateLimitRoutes := map[string]*redisRateLimiter{
		"POST /api/v1/user/login":    newRedisRateLimiter(rdb, 5, 60),
		"POST /api/v1/user/register": newRedisRateLimiter(rdb, 3, 60),
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		route := routeKey(r.Method, path)

		if _, allowed := noAuthRoutes[route]; allowed {
			if limiter, ok := rateLimitRoutes[route]; ok {
				ip := extractIP(r, trustedProxies)
				if !limiter.allow(r.Context(), "storemicro:rl:"+path+":"+ip) {
					pkglog.L().Warn("rate limited", zap.String("path", path), zap.String("ip", ip))
					writeJSON(w, http.StatusTooManyRequests, `{"code":429,"message":"too many requests"}`)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			pkglog.L().Warn("jwt auth failed: missing Authorization header",
				zap.String("path", path),
				zap.String("remote", r.RemoteAddr),
			)
			writeJSON(w, http.StatusUnauthorized, `{"code":401,"message":"missing token"}`)
			return
		}

		tokenStr := authHeader
		if len(authHeader) > 6 {
			// 支持多种 Bearer 格式（大小写不敏感）
			prefix := strings.ToLower(authHeader[:7])
			if prefix == "bearer " {
				tokenStr = strings.TrimSpace(authHeader[7:])
			} else if strings.ToLower(authHeader[:6]) == "bearer" {
				// "Bearer" 无空格变体，视为 token 本身就是 "Bearer"
				tokenStr = authHeader
			}
		}

		token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			pkglog.L().Warn("jwt auth failed: invalid token",
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

		val, err := rdb.Get(r.Context(), "storemicro:token:"+tokenStr).Result()
		if err == redis.Nil {
			pkglog.L().Warn("jwt auth failed: token revoked (logged in elsewhere)",
				zap.String("path", path),
				zap.Int64("user_id", claims.UserID),
			)
			writeJSON(w, http.StatusUnauthorized, `{"code":401,"message":"token expired, please re-login"}`)
			return
		} else if err != nil {
			pkglog.L().Error("redis check token failed, fail-closed",
				zap.String("path", path),
				zap.Int64("user_id", claims.UserID),
				zap.Error(err),
			)
			writeJSON(w, http.StatusServiceUnavailable, `{"code":503,"message":"service temporarily unavailable"}`)
			return
		}
		_ = val

		ctx := context.WithValue(r.Context(), ContextKeyUserID, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func routeKey(method, path string) string {
	return method + " " + path
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func extractIP(r *http.Request, trustedProxies []string) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	if isTrustedProxy(remoteHost, trustedProxies) {
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
	}

	return remoteHost
}

func isTrustedProxy(ip string, trustedProxies []string) bool {
	remoteIP := net.ParseIP(ip)
	if remoteIP == nil {
		return ip == "localhost"
	}
	if remoteIP.IsLoopback() {
		return true
	}

	for _, proxy := range trustedProxies {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			continue
		}
		if _, ipNet, err := net.ParseCIDR(proxy); err == nil {
			if ipNet.Contains(remoteIP) {
				return true
			}
			continue
		}
		if trustedIP := net.ParseIP(proxy); trustedIP != nil && trustedIP.Equal(remoteIP) {
			return true
		}
	}
	return false
}
