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
	"go.uber.org/zap"
)

// securityHeadersMiddleware 添加安全响应头，防止常见的 Web 攻击
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Set("Cache-Control", "no-store")
		// HSTS：强制 HTTPS（仅当服务通过 HTTPS 提供时生效，不影响 HTTP）
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

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

// Claims JWT claims（只含 user_id，不存 phone 等 PII）
type Claims struct {
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

// contextKey 自定义类型用于 context key，避免与其他包的 key 碰撞
type contextKey string

const (
	ContextKeyUserID contextKey = "user_id"
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
		// Redis 异常时 fail-closed：拒绝请求并记录告警，防止被踢掉的旧 token 继续使用
		zapLog.Error("rate limiter redis error, fail-closed",
			zap.String("key", key),
			zap.Error(err),
		)
		return false
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
			// Redis 故障时 fail-closed：拒绝请求，避免被踢掉的 token 继续使用
			// 这比 fail-open 更安全，但需要运维保证 Redis 高可用
			zapLog.Error("redis check token failed, fail-closed",
				zap.String("path", path),
				zap.Int64("user_id", claims.UserID),
				zap.Error(err),
			)
			writeJSON(w, http.StatusServiceUnavailable, `{"code":503,"message":"service temporarily unavailable"}`)
			return
		} else {
			_ = val // val 是 userID，可按需记录
		}

		// 将 user_id 写入 context（不再写入 phone，避免 PII 泄露）
		ctx := context.WithValue(r.Context(), ContextKeyUserID, claims.UserID)
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

// extractIP 提取客户端真实 IP
// 使用可信代理 IP 白名单来验证 X-Forwarded-For，防止 IP 伪造
// 如果 RemoteAddr 不在白名单中（即直连客户端），直接使用 RemoteAddr
// 如果在白名单中（经过可信代理），取 X-Forwarded-For 最左侧 IP
// 生产环境应通过配置注入 trustedProxySubnets
func extractIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	// 检查是否来自可信代理（localhost / 内网）
	if isTrustedProxy(remoteHost) {
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

// isTrustedProxy 检查 IP 是否来自可信代理（本地回环 + RFC 1918 私有网段）
func isTrustedProxy(ip string) bool {
	switch {
	case ip == "127.0.0.1", ip == "::1", ip == "localhost":
		return true
	case strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "192.168."):
		return true
	case strings.HasPrefix(ip, "172."):
		// 172.16.0.0/12
		parts := strings.SplitN(ip, ".", 4)
		if len(parts) >= 2 {
			second := 0
			for _, c := range parts[1] {
				if c < '0' || c > '9' {
					return false
				}
				second = second*10 + int(c-'0')
			}
			if second >= 16 && second <= 31 {
				return true
			}
		}
	}
	return false
}
