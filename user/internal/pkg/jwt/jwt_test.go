package jwt

import (
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

const testSecret = "this-is-a-test-secret-key-with-32-chars!!"

func TestNewJWT(t *testing.T) {
	// secret too short should panic
	assert.Panics(t, func() { NewJWT("short", 2) }, "secret < 32 should panic")

	// expireHrs <= 0 should panic
	assert.Panics(t, func() { NewJWT(testSecret, 0) }, "expireHrs=0 should panic")
	assert.Panics(t, func() { NewJWT(testSecret, -1) }, "negative expireHrs should panic")

	// expireHrs > 24 should panic
	assert.Panics(t, func() { NewJWT(testSecret, 25) }, "expireHrs>24 should panic")

	// valid creation
	j := NewJWT(testSecret, 2)
	assert.NotNil(t, j)
	assert.Equal(t, testSecret, string(j.secret))
	assert.Equal(t, int64(2), j.expireHrs)
	assert.Equal(t, "storemicro-auth", j.issuer)
}

func TestGenerateToken(t *testing.T) {
	j := NewJWT(testSecret, 2)
	token, err := j.GenerateToken(1001)
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestGenerateToken_Expired(t *testing.T) {
	// Create JWT with 1 hour expiry
	j := NewJWT(testSecret, 1)
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
	j := NewJWT(testSecret, 2)
	token, err := j.GenerateRefreshToken(999)
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestParseRefreshToken_Valid(t *testing.T) {
	j := NewJWT(testSecret, 2)
	refreshToken, err := j.GenerateRefreshToken(12345)
	assert.NoError(t, err)

	claims, err := j.ParseRefreshToken(refreshToken)
	assert.NoError(t, err)
	assert.NotNil(t, claims)
	assert.Equal(t, int64(12345), claims.UserID)
}

func TestParseRefreshToken_InvalidSignature(t *testing.T) {
	j := NewJWT(testSecret, 2)
	other := NewJWT("another-different-secret-key-32-chars-ok!", 2)
	refreshToken, err := other.GenerateRefreshToken(1)
	assert.NoError(t, err)

	_, err = j.ParseRefreshToken(refreshToken)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid refresh token")
}

func TestParseRefreshToken_WrongAudience(t *testing.T) {
	j := NewJWT(testSecret, 2)
	// Generate access token (audience="storemicro-api") and parse as refresh token
	accessToken, err := j.GenerateToken(1)
	assert.NoError(t, err)

	_, err = j.ParseRefreshToken(accessToken)
	assert.Error(t, err)
}

func TestParseRefreshToken_Expired(t *testing.T) {
	// We need to create a token that's already expired.
	// Since we can't set negative expireHours, we create JWT manually with a past expiry.
	j := NewJWT(testSecret, 2)

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

func TestExpireHours(t *testing.T) {
	j := NewJWT(testSecret, 5)
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
