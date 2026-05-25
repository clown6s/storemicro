package jwt

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
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

// SigningMethod 支持的签名算法
type SigningMethod string

const (
	SigningMethodHS256 SigningMethod = "HS256"
	SigningMethodRS256 SigningMethod = "RS256"
	SigningMethodES256 SigningMethod = "ES256"
)

// JWT 工具
type JWT struct {
	secret        []byte        // HS256 密钥
	refreshSecret []byte        // Refresh Token 独立密钥（HS256），为空则复用 secret
	method        SigningMethod // 签名算法
	privateKey    any           // RS256/ES256 私钥（*rsa.PrivateKey 或 *ecdsa.PrivateKey）
	expireHrs     int64
	issuer        string
}

// NewJWT 创建 JWT 工具（HS256 模式）
// refreshSecret 为空时复用 secret 签发 Refresh Token
func NewJWT(secret string, refreshSecret string, expireHrs int64) *JWT {
	return newJWTWithOptions(secret, refreshSecret, expireHrs, SigningMethodHS256, "")
}

// NewJWTWithKey 创建 JWT 工具（RS256/ES256 模式，需指定私钥文件路径）
// refreshSecret 在非对称算法下被忽略（Refresh Token 使用相同私钥签名）
func NewJWTWithKey(secret string, expireHrs int64, method SigningMethod, privateKeyPath string) (*JWT, error) {
	if method == SigningMethodHS256 {
		return nil, fmt.Errorf("use NewJWT for HS256, not NewJWTWithKey")
	}
	return newJWTWithOptions(secret, "", expireHrs, method, privateKeyPath), nil
}

func newJWTWithOptions(secret, refreshSecret string, expireHrs int64, method SigningMethod, privateKeyPath string) *JWT {
	if method != SigningMethodHS256 && privateKeyPath == "" {
		panic(fmt.Sprintf("jwt: private key path required for %s", method))
	}
	if expireHrs <= 0 {
		panic("jwt expire_hours must be positive")
	}
	if expireHrs > 24 {
		panic("jwt expire_hours too large (max 24 = 1 day)")
	}

	j := &JWT{
		secret:    []byte(secret),
		expireHrs: expireHrs,
		issuer:    "storemicro-auth",
		method:    method,
	}

	// HS256: 校验 secret 长度
	if method == SigningMethodHS256 {
		if len(secret) < 32 {
			panic("jwt secret must be at least 32 characters")
		}
		// Refresh Token 独立密钥，为空则复用 access secret
		if refreshSecret != "" {
			if len(refreshSecret) < 32 {
				panic("jwt refresh secret must be at least 32 characters")
			}
			j.refreshSecret = []byte(refreshSecret)
		} else {
			j.refreshSecret = j.secret
		}
	} else {
		// RS256/ES256: 从文件加载私钥
		j.privateKey = loadPrivateKey(privateKeyPath)
		j.refreshSecret = j.secret // 非对称模式下 refresh 标记（实际用 private key 签名）
	}

	return j
}

// loadPrivateKey 从 PEM 文件加载私钥
func loadPrivateKey(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("jwt: failed to read private key file %s: %v", path, err))
	}

	block, _ := pem.Decode(data)
	if block == nil {
		panic(fmt.Sprintf("jwt: no PEM block found in %s", path))
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key // RSA
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key // RSA or ECDSA (PKCS8)
	}

	panic(fmt.Sprintf("jwt: failed to parse private key from %s (not PKCS1 or PKCS8)", path))
}

// refreshUsesSeparateKey 标记 Refresh Token 是否使用独立密钥
func (j *JWT) refreshUsesSeparateKey() bool {
	return string(j.refreshSecret) != string(j.secret)
}

// signingKey 返回签名密钥和算法
func (j *JWT) signingKey(useRefresh bool) (any, jwt.SigningMethod) {
	switch j.method {
	case SigningMethodRS256:
		return j.privateKey, jwt.SigningMethodRS256
	case SigningMethodES256:
		return j.privateKey, jwt.SigningMethodES256
	default: // HS256
		if useRefresh {
			return j.refreshSecret, jwt.SigningMethodHS256
		}
		return j.secret, jwt.SigningMethodHS256
	}
}

// GenerateToken 生成 JWT token（只包含 user_id，不包含 phone 等 PII 信息）
func (j *JWT) GenerateToken(userID int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    j.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			Audience:  jwt.ClaimStrings{"storemicro-api"},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(j.expireHrs) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	key, method := j.signingKey(false)
	token := jwt.NewWithClaims(method, claims)
	return token.SignedString(key)
}

// ParseRefreshToken 解析并校验 refresh token（签名 + audience + 过期时间）
func (j *JWT) ParseRefreshToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
		// 校验签名算法是否匹配配置
		_, method := j.signingKey(true)
		if token.Method.Alg() != method.Alg() {
			return nil, jwt.ErrSignatureInvalid
		}
		key, _ := j.signingKey(true)
		return key, nil
	}, jwt.WithAudience("storemicro-refresh"), jwt.WithIssuer(j.issuer))
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}

	c, ok := token.Claims.(*Claims)
	if !ok {
		return nil, fmt.Errorf("invalid refresh token claims")
	}

	// 防御性过期校验：jwt.ParseWithClaims 已内置过期检查，此处作为额外防线
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
	key, method := j.signingKey(true)
	token := jwt.NewWithClaims(method, claims)
	return token.SignedString(key)
}
