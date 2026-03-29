package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListSuppliers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var suppliers []models.Supplier
		if err := db.Where("tenant_id = ?", tenantID).
			Order("company_name ASC").
			Find(&suppliers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener proveedores"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": suppliers})
	}
}

func CreateSupplier(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID          string `json:"id"`
		CompanyName string `json:"company_name" binding:"required"`
		ContactName string `json:"contact_name"`
		Phone       string `json:"phone"        binding:"required"`
		Emoji       string `json:"emoji"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID v4 válido"})
			return
		}

		supplier := models.Supplier{
			TenantID:    tenantID,
			CompanyName: req.CompanyName,
			ContactName: req.ContactName,
			Phone:       req.Phone,
			Emoji:       req.Emoji,
		}
		if req.ID != "" {
			supplier.ID = req.ID
		}

		if err := db.Create(&supplier).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear proveedor"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": supplier})
	}
}

func UpdateSupplier(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		CompanyName *string `json:"company_name"`
		ContactName *string `json:"contact_name"`
		Phone       *string `json:"phone"`
		Emoji       *string `json:"emoji"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var supplier models.Supplier
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&supplier).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "proveedor no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.CompanyName != nil {
			updates["company_name"] = *req.CompanyName
		}
		if req.ContactName != nil {
			updates["contact_name"] = *req.ContactName
		}
		if req.Phone != nil {
			updates["phone"] = *req.Phone
		}
		if req.Emoji != nil {
			updates["emoji"] = *req.Emoji
		}

		if err := db.Model(&supplier).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar proveedor"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": supplier})
	}
}

func DeleteSupplier(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		result := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).Delete(&models.Supplier{})
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "proveedor no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "proveedor eliminado"})
	}
}

func SupplierOrderWA(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ProductName string `json:"product_name" binding:"required"`
		Quantity    int    `json:"quantity"      binding:"required,min=1"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var supplier models.Supplier
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&supplier).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "proveedor no encontrado"})
			return
		}

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener datos del negocio"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		waSvc := services.NewWhatsAppService()
		message := waSvc.SupplierOrder(supplier.ContactName, req.ProductName, req.Quantity, tenant.OwnerName)
		waURL := waSvc.BuildURL(supplier.Phone, message)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"whatsapp_url": waURL,
				"message":      message,
			},
		})
	}
}
