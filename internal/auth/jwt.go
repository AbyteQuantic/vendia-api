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

type Claims struct {
	TenantID     string `json:"tenant_id"`
	Phone        string `json:"phone"`
	BusinessName string `json:"business_name"`
	IsSuperAdmin bool   `json:"is_super_admin,omitempty"`
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

func GenerateAdminToken(adminID, email, name, secret string) (string, error) {
	claims := Claims{
		TenantID:     adminID,
		Phone:        email,
		BusinessName: name,
		IsSuperAdmin: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenDuration)),
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
