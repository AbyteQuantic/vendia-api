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
	// CreditLabelMode is the vocabulary mode for fiar/crédito copy (Spec F028).
	// Surfaced at login so the Flutter app can cache it alongside feature_flags
	// and use it offline without an extra round-trip (FR-05, NFR offline).
	CreditLabelMode string `json:"credit_label_mode"`
	// Spec 051 — capacidades opcionales que viven como COLUMNAS top-level del
	// tenant (no dentro del JSONB feature_flags). Antes el login solo emitía el
	// JSONB, así que el dashboard degradaba una capacidad ACTIVA (Recetas,
	// Marketing, etc.) a "Descubre más opciones" tras cada login. La app las
	// mergea en FeatureFlags (_saveFeatureFlags topLevelKeys). Siempre se
	// emiten (true y false) para encender Y apagar correctamente.
	EnableRecipes            bool `json:"enable_recipes"`
	EnableMarketingHub       bool `json:"enable_marketing_hub"`
	EnableQuotes             bool `json:"enable_quotes"`
	EnablePromotions         bool `json:"enable_promotions"`
	EnableCustomerManagement bool `json:"enable_customer_management"`
	EnableSupplies           bool `json:"enable_supplies"`
	EnableFurnitureJobs      bool `json:"enable_furniture_jobs"`
	EnablePurchaseOrders     bool `json:"enable_purchase_orders"`
	EnablePriceTiers         bool `json:"enable_price_tiers"`
	// Role / BranchID / UserID expose the workspace context the
	// JWT already carries so the client can persist them without
	// decoding the token. Empty on the legacy tenants-only path.
	Role     string `json:"role,omitempty"`
	BranchID string `json:"branch_id,omitempty"`
	UserID   string `json:"user_id,omitempty"`
}

// applyCapabilityFlags copia las columnas de capacidad top-level del tenant a
// la respuesta de auth. Centralizado para que createTokenPair (camino legacy) y
// createWorkspaceTokenPair (camino multi-workspace) no se desincronicen
// (Spec 051).
func applyCapabilityFlags(resp *AuthResponse, t models.Tenant) {
	resp.EnableRecipes = t.EnableRecipes
	resp.EnableMarketingHub = t.EnableMarketingHub
	resp.EnableQuotes = t.EnableQuotes
	resp.EnablePromotions = t.EnablePromotions
	resp.EnableCustomerManagement = t.EnableCustomerManagement
	resp.EnableSupplies = t.EnableSupplies
	resp.EnableFurnitureJobs = t.EnableFurnitureJobs
	resp.EnablePurchaseOrders = t.EnablePurchaseOrders
	resp.EnablePriceTiers = t.EnablePriceTiers
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

	resp := &AuthResponse{
		Token:           accessToken,
		AccessToken:     accessToken,
		RefreshToken:    refreshStr,
		TenantID:        tenant.ID,
		OwnerName:       tenant.OwnerName,
		BusinessName:    tenant.BusinessName,
		BusinessTypes:   tenant.BusinessTypes,
		FeatureFlags:    tenant.FeatureFlags,
		CreditLabelMode: tenant.CreditLabelMode,
	}
	applyCapabilityFlags(resp, tenant)
	return resp, nil
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

		// If the refresh token has a user_id, restore the full workspace
		// context (branch_id, role) so the new JWT keeps the same sede.
		// Without this, the refresh would lose branch isolation and
		// products/sales would land on the wrong branch.
		if rt.UserID != nil && *rt.UserID != "" {
			var user models.User
			if err := db.First(&user, "id = ?", *rt.UserID).Error; err == nil {
				var ws models.UserWorkspace
				if err := db.Where("user_id = ? AND tenant_id = ?",
					user.ID, tenant.ID).First(&ws).Error; err == nil {
					branchID := ""
					if ws.BranchID != nil {
						branchID = *ws.BranchID
					}
					resp, err := createWorkspaceTokenPair(
						db, user, tenant.ID, branchID,
						tenant.BusinessName, string(ws.Role), jwtSecret,
					)
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
						return
					}
					c.JSON(http.StatusOK, resp)
					return
				}
			}
		}

		// Fallback: legacy single-tenant token (no user context)
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
