package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListCustomers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		var total int64
		query := db.Model(&models.Customer{}).Where("tenant_id = ?", tenantID)
		query.Count(&total)

		var customers []models.Customer
		if err := query.
			Order("name ASC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&customers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener clientes"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(customers, total, p))
	}
}

func CreateCustomer(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID    string `json:"id"`
		Name  string `json:"name"  binding:"required,min=2"`
		Phone string `json:"phone"`
		Notes string `json:"notes"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a valid UUID v4"})
			return
		}

		customer := models.Customer{
			TenantID: tenantID,
			Name:     req.Name,
			Phone:    req.Phone,
			Notes:    req.Notes,
		}
		if req.ID != "" {
			customer.ID = req.ID
		}

		if err := db.Create(&customer).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear cliente"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": customer})
	}
}

func UpdateCustomer(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name  *string `json:"name"`
		Phone *string `json:"phone"`
		Notes *string `json:"notes"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		customerID := c.Param("id")

		var customer models.Customer
		if err := db.Where("id = ? AND tenant_id = ?", customerID, tenantID).
			First(&customer).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cliente no encontrado"})
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
		if req.Phone != nil {
			updates["phone"] = *req.Phone
		}
		if req.Notes != nil {
			updates["notes"] = *req.Notes
		}

		if err := db.Model(&customer).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar cliente"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": customer})
	}
}
