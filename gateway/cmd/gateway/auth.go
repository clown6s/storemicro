package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Claims JWT claims，与 user 服务保持一致
type Claims struct {
	UserID int64  `json:"user_id"`
	Phone  string `json:"phone"`
	jwt.RegisteredClaims
}

// noAuthPaths 不需要 JWT 校验的路径（登录、注册、健康检查等）
var noAuthPaths = map[string]bool{
	"/api/v1/user/register": true,
	"/api/v1/user/login":    true,
	"/api/v1/user/ping":     true,
	"/api/v1/product/ping":  true,
}

// jwtAuth JWT 认证中间件（JWT 签名校验 + Redis token 有效性校验，支持单设备登录踢人）
func jwtAuth(secret string, rdb *redis.Client, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 白名单路径直接放行
		if noAuthPaths[path] {
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
			http.Error(w, `{"code":401,"message":"missing token"}`, http.StatusUnauthorized)
			return
		}

		// 支持 Bearer 前缀
		tokenStr := authHeader
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
		}

		// 1) 解析并校验 JWT 签名
		token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			zapLog.Warn("jwt auth failed: invalid token",
				zap.String("path", path),
				zap.Error(err),
			)
			http.Error(w, `{"code":401,"message":"invalid token"}`, http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(*Claims)
		if !ok {
			http.Error(w, `{"code":401,"message":"invalid claims"}`, http.StatusUnauthorized)
			return
		}

		// 2) Redis 校验：token 是否仍然有效（被踢掉的旧 token 会被删除）
		val, err := rdb.Get(r.Context(), "storemicro:token:"+tokenStr).Result()
		if err == redis.Nil {
			zapLog.Warn("jwt auth failed: token revoked (logged in elsewhere)",
				zap.String("path", path),
				zap.Int64("user_id", claims.UserID),
			)
			http.Error(w, `{"code":401,"message":"token expired, please re-login"}`, http.StatusUnauthorized)
			return
		} else if err != nil {
			zapLog.Error("redis check token failed", zap.Error(err))
			// Redis 不可用时降级：仅 JWT 校验通过就放行
		} else {
			_ = val // val 是 userID，可按需记录
		}

		// 将 user_id 写入 context，下游可取用
		ctx := context.WithValue(r.Context(), "user_id", claims.UserID)
		ctx = context.WithValue(ctx, "phone", claims.Phone)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}
