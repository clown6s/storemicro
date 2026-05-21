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

// ExpireHours 返回 token 过期时长（用于 Redis key TTL）
func (j *JWT) ExpireHours() time.Duration {
	return time.Duration(j.expireHrs) * time.Hour
}
