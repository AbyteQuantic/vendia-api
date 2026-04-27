package handlers

import (
	"net/http"
	"time"
	"vendia-backend/internal/auth"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AuthResponse struct {
	Token string `json:"token"`
	// AccessToken is the same JWT under the canonical name newer
	// Flutter clients key on. We populate both so legacy clients
	// reading `token` keep working while the new branch reads
	// `access_token` to distinguish the workspace-aware response
	// from the legacy single-tenant payload.
	AccessToken   string              `json:"access_token"`
	RefreshToken  string              `json:"refresh_token,omitempty"`
	TenantID      string              `json:"tenant_id"`
	OwnerName     string              `json:"owner_name"`
	BusinessName  string              `json:"business_name"`
	BusinessTypes []string            `json:"business_types"`
	FeatureFlags  models.FeatureFlags `json:"feature_flags"`
	// Role / BranchID / UserID expose the workspace context the
	// JWT already carries so the client can persist them without
	// decoding the token. Empty on the legacy tenants-only path.
	Role     string `json:"role,omitempty"`
	BranchID string `json:"branch_id,omitempty"`
	UserID   string `json:"user_id,omitempty"`
}

func createTokenPair(db *gorm.DB, tenant models.Tenant, jwtSecret string) (*AuthResponse, error) {
	accessToken, err := auth.GenerateToken(tenant.ID, tenant.Phone, tenant.BusinessName, jwtSecret)
	if err != nil {
		return nil, err
	}

	refreshStr, err := auth.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	rt := models.RefreshToken{
		TenantID:  tenant.ID,
		Token:     refreshStr,
		ExpiresAt: time.Now().Add(auth.RefreshTokenDuration),
	}
	if err := db.Create(&rt).Error; err != nil {
		return nil, err
	}

	return &AuthResponse{
		Token:         accessToken,
		AccessToken:   accessToken,
		RefreshToken:  refreshStr,
		TenantID:      tenant.ID,
		OwnerName:     tenant.OwnerName,
		BusinessName:  tenant.BusinessName,
		BusinessTypes: tenant.BusinessTypes,
		FeatureFlags:  tenant.FeatureFlags,
	}, nil
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func RefreshToken(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req refreshRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var rt models.RefreshToken
		if err := db.Where("token = ? AND revoked = false", req.RefreshToken).First(&rt).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "refresh token inválido"})
			return
		}

		if time.Now().After(rt.ExpiresAt) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "refresh token expirado"})
			return
		}

		db.Model(&rt).Update("revoked", true)

		var tenant models.Tenant
		if err := db.First(&tenant, "id = ?", rt.TenantID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant no encontrado"})
			return
		}

		resp, err := createTokenPair(db, tenant, jwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
			return
		}

		c.JSON(http.StatusOK, resp)
	}
}

func Logout(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req refreshRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tenantID := middleware.GetTenantID(c)

		result := db.Model(&models.RefreshToken{}).
			Where("token = ? AND tenant_id = ? AND revoked = false", req.RefreshToken, tenantID).
			Update("revoked", true)

		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "refresh token no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "sesión cerrada correctamente"})
	}
}
