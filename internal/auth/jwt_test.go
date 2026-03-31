package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-that-is-at-least-32-chars-long"

func TestGenerateToken_ProducesValidJWT(t *testing.T) {
	token, err := GenerateToken("550e8400-e29b-41d4-a716-446655440000", "3001234567", "TestBiz", testSecret)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	claims, err := ValidateToken(token, testSecret)
	require.NoError(t, err)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", claims.TenantID)
	assert.Equal(t, "3001234567", claims.Phone)
	assert.Equal(t, "TestBiz", claims.BusinessName)
	assert.Equal(t, "vendia-backend", claims.Issuer)
}

func TestGenerateToken_ExpiresIn7Days(t *testing.T) {
	token, err := GenerateToken("550e8400-e29b-41d4-a716-446655440000", "3001234567", "TestBiz", testSecret)
	require.NoError(t, err)

	claims, err := ValidateToken(token, testSecret)
	require.NoError(t, err)

	expiry := claims.ExpiresAt.Time
	expectedMin := time.Now().Add(7*24*time.Hour - 1*time.Minute)
	expectedMax := time.Now().Add(7*24*time.Hour + 1*time.Minute)
	assert.True(t, expiry.After(expectedMin))
	assert.True(t, expiry.Before(expectedMax))
}

func TestValidateToken_RejectsWrongSecret(t *testing.T) {
	token, err := GenerateToken("550e8400-e29b-41d4-a716-446655440000", "3001234567", "TestBiz", testSecret)
	require.NoError(t, err)

	_, err = ValidateToken(token, "wrong-secret-that-is-at-least-32")
	assert.Error(t, err)
}

func TestGenerateRefreshToken_UniqueAndLongEnough(t *testing.T) {
	t1, err := GenerateRefreshToken()
	require.NoError(t, err)
	assert.Len(t, t1, 64)

	t2, err := GenerateRefreshToken()
	require.NoError(t, err)
	assert.NotEqual(t, t1, t2)
}

func TestGenerateAdminToken_HasSuperAdminClaim(t *testing.T) {
	token, err := GenerateAdminToken("admin-uuid", "admin@vendia.co", "Admin", testSecret)
	require.NoError(t, err)

	claims, err := ValidateToken(token, testSecret)
	require.NoError(t, err)
	assert.True(t, claims.IsSuperAdmin)
	assert.Equal(t, "admin-uuid", claims.TenantID)
}
