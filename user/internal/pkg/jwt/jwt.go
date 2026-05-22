package jwt

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims 自定义 JWT claims（只存 user_id，不存 phone 等 PII）
type Claims struct {
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

// RefreshExpireHours refresh token 有效期（7 天 = 168 小时）
const RefreshExpireHours = 168

// JWT 工具
type JWT struct {
	secret    []byte
	expireHrs int64
	issuer    string
}

// NewJWT 创建 JWT 工具
func NewJWT(secret string, expireHrs int64) *JWT {
	if len(secret) < 32 {
		panic("jwt secret must be at least 32 characters")
	}
	if expireHrs <= 0 {
		panic("jwt expire_hours must be positive")
	}
	if expireHrs > 24 {
		panic("jwt expire_hours too large (max 24 = 1 day)")
	}
	return &JWT{
		secret:    []byte(secret),
		expireHrs: expireHrs,
		issuer:    "storemicro-auth",
	}
}

// GenerateToken 生成 JWT token（只包含 user_id，不包含 phone 等 PII 信息）
func (j *JWT) GenerateToken(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			// [L3] 补全标准 claims
			Issuer:    j.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			Audience:  jwt.ClaimStrings{"storemicro-api"},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(j.expireHrs) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

// ParseRefreshToken 解析并校验 refresh token（签名 + audience + 过期时间）
func (j *JWT) ParseRefreshToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return j.secret, nil
	}, jwt.WithAudience("storemicro-refresh"), jwt.WithIssuer(j.issuer))
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}

	c, ok := token.Claims.(*Claims)
	if !ok {
		return nil, fmt.Errorf("invalid refresh token claims")
	}

	// 防御性过期校验：jwt.ParseWithClaims 已内置过期检查，此处作为额外防线
	// 处理极端情况：token 恰好在 ParseWithClaims 和此处检查之间过期
	if c.ExpiresAt == nil || c.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("refresh token expired")
	}

	return c, nil
}

// ExpireHours 返回 access token 过期时长（用于 Redis key TTL）
func (j *JWT) ExpireHours() time.Duration {
	return time.Duration(j.expireHrs) * time.Hour
}

// GenerateRefreshToken 生成 refresh token（7 天有效期，仅包含 user_id）
func (j *JWT) GenerateRefreshToken(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    j.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			Audience:  jwt.ClaimStrings{"storemicro-refresh"},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(RefreshExpireHours) * time.Hour)), // 7 天
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}
