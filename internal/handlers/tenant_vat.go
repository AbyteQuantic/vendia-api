package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

type updateTenantVATRequest struct {
	VATEnabled          *bool    `json:"vat_enabled"`
	VATRate             *float64 `json:"vat_rate"`
	VATInclusivePricing *bool    `json:"vat_inclusive_pricing"`
	DIANThresholdCOP    *int64   `json:"dian_threshold_cop"`
}

// UpdateTenantVATSettings — PATCH /api/v1/tenant/vat
// Persists the VAT flow configuration server-side so the frontend's
// SharedPreferences can be hydrated from the backend on login.
// Once activated, the tenant gets VATActivatedAt stamped (immutable).
func UpdateTenantVATSettings(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant requerido"})
			return
		}

		var req updateTenantVATRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "datos inválidos"})
			return
		}

		// Validations: rate must be in [0, 0.5] when present.
		if req.VATRate != nil && (*req.VATRate < 0 || *req.VATRate > 0.5) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tasa de IVA fuera de rango"})
			return
		}
		if req.DIANThresholdCOP != nil && *req.DIANThresholdCOP < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "umbral inválido"})
			return
		}

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant no encontrado"})
			return
		}

		updates := map[string]any{}
		if req.VATEnabled != nil {
			updates["vat_enabled"] = *req.VATEnabled
			if *req.VATEnabled && tenant.VATActivatedAt == nil {
				now := time.Now()
				updates["vat_activated_at"] = &now
			}
		}
		if req.VATRate != nil {
			updates["vat_rate"] = *req.VATRate
		}
		if req.VATInclusivePricing != nil {
			updates["vat_inclusive_pricing"] = *req.VATInclusivePricing
		}
		if req.DIANThresholdCOP != nil {
			updates["dian_threshold_cop"] = *req.DIANThresholdCOP
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sin campos para actualizar"})
			return
		}

		if err := db.Model(&tenant).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar"})
			return
		}

		// Re-read to return canonical state.
		db.Where("id = ?", tenantID).First(&tenant)
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"vat_enabled":           derefBool(tenant.VATEnabled),
				"vat_rate":              derefFloat(tenant.VATRate),
				"vat_inclusive_pricing": derefBoolDefault(tenant.VATInclusivePricing, true),
				"vat_activated_at":      tenant.VATActivatedAt,
				"dian_threshold_cop":    derefInt64(tenant.DIANThresholdCOP),
			},
		})
	}
}

// GetTenantVATSettings — GET /api/v1/tenant/vat. Allows the
// frontend to hydrate its SharedPreferences on login.
func GetTenantVATSettings(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant requerido"})
			return
		}
		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"vat_enabled":           derefBool(tenant.VATEnabled),
				"vat_rate":              derefFloat(tenant.VATRate),
				"vat_inclusive_pricing": derefBoolDefault(tenant.VATInclusivePricing, true),
				"vat_activated_at":      tenant.VATActivatedAt,
				"dian_threshold_cop":    derefInt64(tenant.DIANThresholdCOP),
			},
		})
	}
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func derefBoolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
