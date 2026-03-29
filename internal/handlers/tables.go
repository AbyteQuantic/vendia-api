package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListTables(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tables []models.Table
		if err := db.Where("tenant_id = ?", tenantID).
			Order("label ASC").
			Find(&tables).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener mesas"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": tables, "count": len(tables)})
	}
}

func CreateTable(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID    string `json:"id"`
		Label string `json:"label" binding:"required"`
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

		table := models.Table{
			TenantID: tenantID,
			Label:    req.Label,
			IsActive: true,
		}
		if req.ID != "" {
			table.ID = req.ID
		}

		if err := db.Create(&table).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear mesa"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": table})
	}
}

func UpdateTable(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Label    *string `json:"label"`
		IsActive *bool   `json:"is_active"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		tableID := c.Param("id")

		var table models.Table
		if err := db.Where("id = ? AND tenant_id = ?", tableID, tenantID).
			First(&table).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "mesa no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Label != nil {
			updates["label"] = *req.Label
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}

		if err := db.Model(&table).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar mesa"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": table})
	}
}
