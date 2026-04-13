package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListPaymentMethods(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var methods []models.TenantPaymentMethod
		if err := db.Where("tenant_id = ?", tenantID).
			Order("created_at ASC").
			Find(&methods).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener métodos de pago"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": methods, "count": len(methods)})
	}
}

func CreatePaymentMethod(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID             string `json:"id"`
		Name           string `json:"name" binding:"required"`
		AccountDetails string `json:"account_details"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		pm := models.TenantPaymentMethod{
			TenantID:       tenantID,
			Name:           req.Name,
			AccountDetails: req.AccountDetails,
			IsActive:       true,
		}
		if req.ID != "" && models.IsValidUUID(req.ID) {
			pm.ID = req.ID
		}

		if err := db.Create(&pm).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear método de pago"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": pm})
	}
}

func UpdatePaymentMethod(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name           *string `json:"name"`
		AccountDetails *string `json:"account_details"`
		IsActive       *bool   `json:"is_active"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		pmID := c.Param("id")

		var pm models.TenantPaymentMethod
		if err := db.Where("id = ? AND tenant_id = ?", pmID, tenantID).
			First(&pm).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "método de pago no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Name != nil {
			updates["name"] = *req.Name
		}
		if req.AccountDetails != nil {
			updates["account_details"] = *req.AccountDetails
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}

		if err := db.Model(&pm).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar método de pago"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": pm})
	}
}

func DeletePaymentMethod(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		pmID := c.Param("id")

		result := db.Where("id = ? AND tenant_id = ?", pmID, tenantID).
			Delete(&models.TenantPaymentMethod{})
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "método de pago no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "método de pago eliminado"})
	}
}
