package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const AccessTokenDuration = 7 * 24 * time.Hour   // 7 days — zero friction for tiendas
const RefreshTokenDuration = 90 * 24 * time.Hour  // 90 days

// AdminAccessTokenDuration is intentionally much shorter than
// AccessTokenDuration: an admin token grants god-mode over every
// tenant, and super-admin/support staff work from a desk with a
// live connection — the "zero friction for tiendas" rationale for
// the 7-day tenant TTL does not apply to them. A stolen laptop or
// shared session should not grant up to 7 days of full platform
// access with no revocation path short of rotating JWT_SECRET for
// every tenant. 4 hours is a reasonable security/friction trade-off
// for connected support staff (H10).
const AdminAccessTokenDuration = 4 * time.Hour

type Claims struct {
	TenantID     string `json:"tenant_id"`
	Phone        string `json:"phone"`
	BusinessName string `json:"business_name"`
	IsSuperAdmin bool   `json:"is_super_admin,omitempty"`
	// Multi-workspace fields (omitempty for backward compat with old tokens)
	UserID   string `json:"user_id,omitempty"`
	BranchID string `json:"branch_id,omitempty"`
	Role     string `json:"role,omitempty"`
	jwt.RegisteredClaims
}

func GenerateToken(tenantID, phone, businessName, secret string) (string, error) {
	claims := Claims{
		TenantID:     tenantID,
		Phone:        phone,
		BusinessName: businessName,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenDuration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "vendia-backend",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// GenerateWorkspaceToken creates a JWT with full multi-workspace context.
func GenerateWorkspaceToken(userID, tenantID, branchID, phone, businessName, role, secret string) (string, error) {
	claims := Claims{
		TenantID:     tenantID,
		Phone:        phone,
		BusinessName: businessName,
		UserID:       userID,
		BranchID:     branchID,
		Role:         role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenDuration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "vendia-backend",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func GenerateAdminToken(adminID, email, name, secret string) (string, error) {
	claims := Claims{
		TenantID:     adminID,
		Phone:        email,
		BusinessName: name,
		IsSuperAdmin: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AdminAccessTokenDuration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "vendia-backend",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func ValidateToken(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(
		tokenStr,
		&Claims{},
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(secret), nil
		},
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
