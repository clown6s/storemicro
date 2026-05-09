package jwt

import (
	"errors"
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
}

// NewJWT 创建 JWT 工具
func NewJWT(secret string, expireHrs int64) *JWT {
	return &JWT{
		secret:    []byte(secret),
		expireHrs: expireHrs,
	}
}

// GenerateToken 生成 JWT token（只包含 user_id，不包含 phone 等 PII 信息）
func (j *JWT) GenerateToken(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(j.expireHrs) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

// ExpireHours 返回 token 过期时长（用于 Redis key TTL）
func (j *JWT) ExpireHours() time.Duration {
	return time.Duration(j.expireHrs) * time.Hour
}

// ParseToken 解析并验证 JWT token
func (j *JWT) ParseToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
		// 校验签名算法，防止 none 算法伪造 token
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, errors.New("invalid claims")
	}
	return claims, nil
}
