package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetPaymentInfo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tenant models.Tenant
		if err := db.Select("nequi_phone, daviplata_phone").
			First(&tenant, "id = ?", tenantID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"nequi_phone":    tenant.NequiPhone,
			"daviplata_phone": tenant.DaviplataPhone,
		})
	}
}

func UpdatePaymentInfo(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		NequiPhone     *string `json:"nequi_phone"`
		DaviplataPhone *string `json:"daviplata_phone"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.NequiPhone != nil {
			updates["nequi_phone"] = *req.NequiPhone
		}
		if req.DaviplataPhone != nil {
			updates["daviplata_phone"] = *req.DaviplataPhone
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).
			Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar datos de pago"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "datos de pago actualizados"})
	}
}
