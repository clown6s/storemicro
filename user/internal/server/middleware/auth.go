package middleware

import (
	"context"

	"user/internal/pkg/jwt"
	"user/internal/pkg/redis"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 不需要鉴权的 RPC 方法
var whiteList = map[string]bool{
	"/api.user.v1.User/Ping":      true,
	"/api.user.v1.User/Register":  true,
	"/api.user.v1.User/Login":     true,
	"/api.user.v1.User/GetUser":   true,
	"/api.user.v1.User/ListUsers": true,
}

// Auth JWT + Redis 鉴权中间件（Kratos Middleware 格式）
func Auth(j *jwt.JWT, r *redis.Redis) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			// 获取当前 RPC 方法名
			if tr, ok := transport.FromServerContext(ctx); ok {
				operation := tr.Operation()
				if whiteList[operation] {
					return handler(ctx, req)
				}
			}

			// 从 Kratos transport header 取 token
			if tr, ok := transport.FromServerContext(ctx); ok {
				tokenStr := tr.RequestHeader().Get("Authorization")
				if tokenStr == "" {
					zap.L().Warn("auth failed: no token")
					return nil, status.Error(codes.Unauthenticated, "missing token")
				}

				// 支持 Bearer 前缀
				if len(tokenStr) > 7 && tokenStr[:7] == "Bearer " {
					tokenStr = tokenStr[7:]
				}

				// 1) JWT 签名校验
				claims, err := j.ParseToken(tokenStr)
				if err != nil {
					zap.L().Warn("auth failed: invalid jwt", zap.Error(err))
					return nil, status.Error(codes.Unauthenticated, "invalid token")
				}

				// 2) Redis 有效性校验（防止已登出的 token 继续使用）
				_, err = r.GetUserIDByToken(ctx, tokenStr)
				if err != nil {
					zap.L().Warn("auth failed: token not in redis", zap.Error(err))
					return nil, status.Error(codes.Unauthenticated, "token expired or revoked")
				}

				// 把 userID 写入 context
				ctx = context.WithValue(ctx, "user_id", claims.UserID)
				ctx = context.WithValue(ctx, "phone", claims.Phone)
			}

			return handler(ctx, req)
		}
	}
}
