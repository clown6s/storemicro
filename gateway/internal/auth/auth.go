package auth

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	pkglog "github.com/storemicro/pkg/zaplogger"
	"go.uber.org/zap"
)

// RecoverMiddleware 全局 panic recovery，防止未捕获 panic 导致 goroutine 崩溃
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				pkglog.L().Error("panic recovered",
					zap.Any("error", rec),
					zap.Stack("stack"),
				)
				WriteJSON(w, http.StatusInternalServerError, `{"code":500,"message":"internal server error"}`)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		next.ServeHTTP(w, r)
	})
}

func CORSMiddleware(allowOrigins []string) func(http.Handler) http.Handler {
	originSet := make(map[string]struct{}, len(allowOrigins))
	for _, o := range allowOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			pkglog.L().Warn("CORS: wildcard origin '*' is not allowed with credentials, skipping")
			continue
		}
		originSet[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if IsAllowedOrigin(origin, originSet) {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
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

// IsAllowedOrigin 检查 origin 是否在允许列表中，支持通配符子域名匹配 (*.example.com)
func IsAllowedOrigin(origin string, allowed map[string]struct{}) bool {
	// 精确匹配
	if _, ok := allowed[origin]; ok {
		return true
	}

	// 通配符子域名匹配 (*.example.com)
	for a := range allowed {
		if strings.HasPrefix(a, "https://") {
			domain := strings.TrimPrefix(a, "https://")
			if strings.HasPrefix(domain, "*.") {
				baseDomain := domain[2:]
				if strings.HasSuffix(origin, baseDomain) && origin != baseDomain {
					return true
				}
			}
		}
		if strings.HasPrefix(a, "http://") {
			domain := strings.TrimPrefix(a, "http://")
			if strings.HasPrefix(domain, "*.") {
				baseDomain := domain[2:]
				if strings.HasSuffix(origin, baseDomain) && origin != baseDomain {
					return true
				}
			}
		}
	}
	return false
}

// Claims JWT claims
type Claims struct {
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

// ContextKey context key 类型
type ContextKey string

// ContextKeyUserID 用户ID context key
const ContextKeyUserID ContextKey = "user_id"

// NoAuthRoutes 免认证路由白名单
var NoAuthRoutes = map[string]struct{}{
	"POST /api/v1/user/register": {},
	"POST /api/v1/user/login":    {},
	"GET /healthz":               {},
}

// RateLimitScript Lua 脚本原子化限流操作
var RateLimitScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '0', ARGV[1])
local count = redis.call('ZCARD', KEYS[1])
if tonumber(count) >= tonumber(ARGV[3]) then
    return count
end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[2] .. '-' .. ARGV[5])
redis.call('EXPIRE', KEYS[1], ARGV[4])
return count + 1
`)

// RateLimiter Redis 滑动窗口限流器
type RateLimiter struct {
	Rdb         *redis.Client
	MaxRequests int64
	WindowSec   int64
}

// NewRateLimiter 创建限流器
func NewRateLimiter(rdb *redis.Client, max, window int) *RateLimiter {
	return &RateLimiter{
		Rdb:         rdb,
		MaxRequests: int64(max),
		WindowSec:   int64(window),
	}
}

// Allow 检查是否允许请求通过
func (rl *RateLimiter) Allow(ctx context.Context, key string) bool {
	now := time.Now().UnixMilli()
	windowStart := now - rl.WindowSec*1000

	var randBytes [8]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		pkglog.L().Warn("rate limiter: crypto/rand failed, using fallback",
			zap.String("key", key),
			zap.Error(err),
		)
		randBytes[0] = byte(now & 0xFF)
		randBytes[1] = byte((now >> 8) & 0xFF)
	}
	randStr := fmt.Sprintf("%016x", binary.BigEndian.Uint64(randBytes[:]))

	result, err := RateLimitScript.Run(ctx, rl.Rdb,
		[]string{key},
		strconv.FormatInt(windowStart, 10),
		strconv.FormatInt(now, 10),
		strconv.FormatInt(rl.MaxRequests, 10),
		strconv.FormatInt(rl.WindowSec, 10),
		randStr,
	).Int64()

	if err != nil {
		pkglog.L().Error("rate limiter redis error, fail-closed",
			zap.String("key", key),
			zap.Error(err),
		)
		return false
	}
	return result <= rl.MaxRequests
}

// JWTAuth JWT 认证 + 限流中间件
func JWTAuth(secret string, rdb *redis.Client, trustedProxies []string, next http.Handler) http.Handler {
	rateLimitRoutes := map[string]*RateLimiter{
		"POST /api/v1/user/login":    NewRateLimiter(rdb, 5, 60),
		"POST /api/v1/user/register": NewRateLimiter(rdb, 3, 60),
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawPath := r.URL.Path
		cleaned := path.Clean(rawPath)
		route := RouteKey(r.Method, cleaned)

		if IsNoAuthRoute(r.Method, rawPath) {
			if limiter, ok := rateLimitRoutes[route]; ok {
				ip := ExtractIP(r, trustedProxies)
				if !limiter.Allow(r.Context(), "storemicro:rl:"+cleaned+":"+ip) {
					pkglog.L().Warn("rate limited", zap.String("path", cleaned), zap.String("ip", MaskIP(ip)))
					WriteJSON(w, http.StatusTooManyRequests, `{"code":429,"message":"too many requests"}`)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			pkglog.L().Warn("jwt auth failed: missing Authorization header",
				zap.String("path", cleaned),
				zap.String("remote", r.RemoteAddr),
			)
			WriteJSON(w, http.StatusUnauthorized, `{"code":401,"message":"missing token"}`)
			return
		}

		tokenStr := authHeader
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			tokenStr = strings.TrimSpace(authHeader[7:])
		}

		token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secret), nil
		}, jwt.WithAudience("storemicro-api"))
		if err != nil || !token.Valid {
			pkglog.L().Warn("jwt auth failed: invalid token",
				zap.String("path", cleaned),
				zap.Error(err),
			)
			WriteJSON(w, http.StatusUnauthorized, `{"code":401,"message":"invalid token"}`)
			return
		}

		claims, ok := token.Claims.(*Claims)
		if !ok {
			WriteJSON(w, http.StatusUnauthorized, `{"code":401,"message":"invalid claims"}`)
			return
		}

		// Redis token 验证使用独立超时，防止 Redis 慢查询卡住认证流程
		redisCtx, redisCancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer redisCancel()

		val, err := rdb.Get(redisCtx, "storemicro:token:"+tokenStr).Result()
		if err == redis.Nil {
			pkglog.L().Warn("jwt auth failed: token revoked (logged in elsewhere)",
				zap.String("path", cleaned),
				zap.Int64("user_id", claims.UserID),
			)
			WriteJSON(w, http.StatusUnauthorized, `{"code":401,"message":"token expired, please re-login"}`)
			return
		} else if err != nil {
			pkglog.L().Error("redis check token failed, fail-closed",
				zap.String("path", cleaned),
				zap.Int64("user_id", claims.UserID),
				zap.Error(err),
			)
			WriteJSON(w, http.StatusServiceUnavailable, `{"code":503,"message":"service temporarily unavailable"}`)
			return
		}

		// 校验 Redis 中 token 对应的 userID 与 JWT claims 一致
		redisUserID, parseErr := strconv.ParseInt(val, 10, 64)
		if parseErr != nil || redisUserID != claims.UserID {
			pkglog.L().Warn("jwt auth failed: token userID mismatch",
				zap.Int64("jwt_user_id", claims.UserID),
				zap.String("redis_value", val),
			)
			WriteJSON(w, http.StatusUnauthorized, `{"code":401,"message":"invalid token"}`)
			return
		}

		ctx := context.WithValue(r.Context(), ContextKeyUserID, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RouteKey 生成路由键
func RouteKey(method, path string) string {
	return method + " " + path
}

// IsNoAuthRoute 检查请求是否在免认证白名单中
func IsNoAuthRoute(method, rawPath string) bool {
	unescaped, err := url.PathUnescape(rawPath)
	if err != nil {
		return false
	}
	cleaned := path.Clean(unescaped)
	_, allowed := NoAuthRoutes[method+" "+cleaned]
	return allowed
}

// WriteJSON 写入 JSON 响应
func WriteJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// ExtractIP 从请求中提取客户端 IP（支持可信代理 X-Forwarded-For）
func ExtractIP(r *http.Request, trustedProxies []string) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	if isTrustedProxy(remoteHost, trustedProxies) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ips := strings.Split(xff, ",")
			for i := len(ips) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(ips[i])
				if ip == "" || net.ParseIP(ip) == nil {
					continue
				}
				if !isTrustedProxy(ip, trustedProxies) {
					return ip
				}
			}
			for _, ip := range ips {
				ip = strings.TrimSpace(ip)
				if ip != "" && net.ParseIP(ip) != nil {
					return ip
				}
			}
		}
		if rip := r.Header.Get("X-Real-IP"); rip != "" {
			rip = strings.TrimSpace(rip)
			if net.ParseIP(rip) != nil {
				return rip
			}
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

// MaskIP 脱敏 IP 地址：保留前两个八位组，隐藏后两部分 (GDPR 合规)
func MaskIP(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	if v4 := parsed.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.x.x", v4[0], v4[1])
	}
	if len(parsed) >= 4 {
		return fmt.Sprintf("%x:%x:%x:%x:x:x:x:x", parsed[0], parsed[1], parsed[2], parsed[3])
	}
	return "masked"
}
