package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
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

// jwtAuth JWT 认证中间件
func jwtAuth(secret string, next http.Handler) http.Handler {
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

		// 解析并校验 JWT
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

		// 将 user_id 写入 context，下游可取用
		if claims, ok := token.Claims.(*Claims); ok {
			ctx := context.WithValue(r.Context(), "user_id", claims.UserID)
			ctx = context.WithValue(ctx, "phone", claims.Phone)
			r = r.WithContext(ctx)
		}

		next.ServeHTTP(w, r)
	})
}
