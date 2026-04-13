package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetBusinessProfile returns the current tenant's business profile data.
// GET /api/v1/store/profile
func GetBusinessProfile(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name": tenant.BusinessName,
				"business_type": tenant.BusinessType,
				"nit":           tenant.NIT,
				"razon_social":  tenant.RazonSocial,
				"address":       tenant.Address,
				"logo_url":      tenant.LogoURL,
				"owner_name":    tenant.OwnerName,
				"phone":         tenant.Phone,
			},
		})
	}
}

// UpdateBusinessProfile partially updates the tenant's business profile.
// PATCH /api/v1/store/profile
func UpdateBusinessProfile(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		BusinessName *string              `json:"business_name"`
		BusinessType *models.BusinessType `json:"business_type"`
		NIT          *string              `json:"nit"`
		RazonSocial  *string              `json:"razon_social"`
		Address      *string              `json:"address"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.BusinessName != nil {
			if *req.BusinessName == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el nombre del negocio es obligatorio"})
				return
			}
			updates["business_name"] = *req.BusinessName
		}
		if req.BusinessType != nil {
			updates["business_type"] = *req.BusinessType
		}
		if req.NIT != nil {
			updates["nit"] = *req.NIT
		}
		if req.RazonSocial != nil {
			updates["razon_social"] = *req.RazonSocial
		}
		if req.Address != nil {
			updates["address"] = *req.Address
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no hay campos para actualizar"})
			return
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar perfil"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "perfil actualizado correctamente"})
	}
}
