package jwt

import (
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

const testSecret = "this-is-a-test-secret-key-with-32-chars!!"
const testRefreshSecret = "this-is-a-test-refresh-secret-key-32chars!!"

func TestNewJWT(t *testing.T) {
	// secret too short should panic
	assert.Panics(t, func() { NewJWT("short", "", 2) }, "secret < 32 should panic")

	// expireHrs <= 0 should panic
	assert.Panics(t, func() { NewJWT(testSecret, "", 0) }, "expireHrs=0 should panic")
	assert.Panics(t, func() { NewJWT(testSecret, "", -1) }, "negative expireHrs should panic")

	// expireHrs > 24 should panic
	assert.Panics(t, func() { NewJWT(testSecret, "", 25) }, "expireHrs>24 should panic")

	// refresh secret too short should panic
	assert.Panics(t, func() { NewJWT(testSecret, "short", 2) }, "refreshSecret < 32 should panic")

	// valid creation
	j := NewJWT(testSecret, "", 2)
	assert.NotNil(t, j)
	assert.Equal(t, testSecret, string(j.secret))
	assert.Equal(t, testSecret, string(j.refreshSecret)) // empty refreshSecret → fallback to secret
	assert.Equal(t, int64(2), j.expireHrs)
	assert.Equal(t, "storemicro-auth", j.issuer)

	// valid creation with separate refresh secret
	j2 := NewJWT(testSecret, testRefreshSecret, 2)
	assert.NotNil(t, j2)
	assert.Equal(t, testSecret, string(j2.secret))
	assert.Equal(t, testRefreshSecret, string(j2.refreshSecret))
	assert.Equal(t, int64(2), j2.expireHrs)
}

func TestGenerateToken(t *testing.T) {
	j := NewJWT(testSecret, "", 2)
	token, err := j.GenerateToken(1001)
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestGenerateToken_Expired(t *testing.T) {
	// Create JWT with 1 hour expiry
	j := NewJWT(testSecret, "", 1)
	token, err := j.GenerateToken(42)
	assert.NoError(t, err)

	// Parse with standard jwt to verify claims
	parsed, err := parseWithClaims(token, j.secret)
	assert.NoError(t, err)
	assert.Equal(t, int64(42), parsed.UserID)
	assert.NotNil(t, parsed.ExpiresAt)
	// Expiry should be ~1 hour from now
	assert.WithinDuration(t, time.Now().Add(1*time.Hour), parsed.ExpiresAt.Time, 5*time.Second)
}

func TestGenerateRefreshToken(t *testing.T) {
	j := NewJWT(testSecret, "", 2)
	token, err := j.GenerateRefreshToken(999)
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestParseRefreshToken_Valid(t *testing.T) {
	j := NewJWT(testSecret, "", 2)
	refreshToken, err := j.GenerateRefreshToken(12345)
	assert.NoError(t, err)

	claims, err := j.ParseRefreshToken(refreshToken)
	assert.NoError(t, err)
	assert.NotNil(t, claims)
	assert.Equal(t, int64(12345), claims.UserID)
}

func TestParseRefreshToken_InvalidSignature_SameSecret(t *testing.T) {
	j := NewJWT(testSecret, "", 2)
	other := NewJWT("another-different-secret-key-32-chars-ok!", "", 2)
	refreshToken, err := other.GenerateRefreshToken(1)
	assert.NoError(t, err)

	_, err = j.ParseRefreshToken(refreshToken)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid refresh token")
}

func TestParseRefreshToken_InvalidSignature_SeparateRefreshSecret(t *testing.T) {
	j := NewJWT(testSecret, testRefreshSecret, 2)

	// 用 refresh secret 生成，但用 access secret（另一个 JWT 实例同 refresh secret）的 ParseRefreshToken 验证
	other := NewJWT("another-access-secret-key-32-chars!", testRefreshSecret, 2)
	refreshToken, err := other.GenerateRefreshToken(1)
	assert.NoError(t, err)

	// j 的 refreshSecret 和 other 的 refreshSecret 相同，应该能解析
	claims, err := j.ParseRefreshToken(refreshToken)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), claims.UserID)

	// 用不同 refresh secret 的 JWT 解析应该失败
	j3 := NewJWT(testSecret, "totally-different-refresh-secret-32c", 2)
	_, err = j3.ParseRefreshToken(refreshToken)
	assert.Error(t, err)
}

func TestParseRefreshToken_WrongAudience(t *testing.T) {
	j := NewJWT(testSecret, "", 2)
	// Generate access token (audience="storemicro-api") and parse as refresh token
	accessToken, err := j.GenerateToken(1)
	assert.NoError(t, err)

	_, err = j.ParseRefreshToken(accessToken)
	assert.Error(t, err)
}

func TestParseRefreshToken_Expired(t *testing.T) {
	// We need to create a token that's already expired.
	j := NewJWT(testSecret, "", 2)

	// Create an expired refresh token by manually building claims
	expiredClaims := Claims{
		UserID:           1,
		RegisteredClaims: buildRegisteredClaims(j, "storemicro-refresh", time.Now().Add(-1*time.Hour)),
	}
	tokenStr := signToken(expiredClaims, j.secret)
	assert.NotEmpty(t, tokenStr)

	_, err := j.ParseRefreshToken(tokenStr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestParseRefreshToken_AccessTokenCannotForgeRefresh(t *testing.T) {
	// 使用独立 refresh secret 时，access secret 无法签发有效的 refresh token
	j := NewJWT(testSecret, testRefreshSecret, 2)

	// 手动用 access secret 签发一个 audience=storemicro-refresh 的 token
	fakeRefreshClaims := Claims{
		UserID: 42,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    j.issuer,
			Subject:   "42",
			Audience:  jwt.ClaimStrings{"storemicro-refresh"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, fakeRefreshClaims)
	fakeToken, _ := token.SignedString([]byte(testSecret)) // 用 access secret 签名

	// 用正确的 refresh secret 验证应该失败
	_, err := j.ParseRefreshToken(fakeToken)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid refresh token")
}

func TestExpireHours(t *testing.T) {
	j := NewJWT(testSecret, "", 5)
	assert.Equal(t, 5*time.Hour, j.ExpireHours())
}

func TestRefreshExpireHours(t *testing.T) {
	assert.Equal(t, 168, RefreshExpireHours)
}

// --- test helpers ---

// parseWithClaims parses a JWT token string using the raw jwt library for claim verification
func parseWithClaims(tokenStr string, secret []byte) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
		return secret, nil
	})
	if err != nil || !token.Valid {
		return nil, err
	}
	c, ok := token.Claims.(*Claims)
	if !ok {
		return nil, fmt.Errorf("invalid claims type")
	}
	return c, nil
}

// buildRegisteredClaims constructs RegisteredClaims for testing
func buildRegisteredClaims(j *JWT, audience string, expiresAt time.Time) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		Issuer:    j.issuer,
		Subject:   "1",
		Audience:  jwt.ClaimStrings{audience},
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		NotBefore: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
	}
}

// signToken signs Claims into a JWT string for testing
func signToken(claims Claims, secret []byte) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := token.SignedString(secret)
	return s
}
