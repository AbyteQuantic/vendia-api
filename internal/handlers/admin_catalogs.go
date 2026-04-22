package handlers

import (
	"net/http"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func AdminListCatalogTemplates(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var templates []models.CatalogTemplate
		if err := db.Order("created_at desc").Find(&templates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al listar plantillas"})
			return
		}
		c.JSON(http.StatusOK, templates)
	}
}

func AdminCreateCatalogTemplate(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var template models.CatalogTemplate
		if err := c.ShouldBindJSON(&template); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if err := db.Create(&template).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear plantilla"})
			return
		}
		c.JSON(http.StatusCreated, template)
	}
}

func AdminUpdateCatalogTemplate(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var template models.CatalogTemplate
		if err := db.First(&template, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "plantilla no encontrada"})
			return
		}

		if err := c.ShouldBindJSON(&template); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if err := db.Save(&template).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar plantilla"})
			return
		}
		c.JSON(http.StatusOK, template)
	}
}

func AdminDeleteCatalogTemplate(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if err := db.Delete(&models.CatalogTemplate{}, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar plantilla"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "plantilla eliminada"})
	}
}

func AdminGetCatalogAnalytics(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		type resultRow struct {
			models.CatalogAnalytics
			BusinessName string `json:"business_name"`
		}
		var rows []resultRow

		err := db.Table("catalog_analytics").
			Select("catalog_analytics.*, tenants.business_name").
			Joins("join tenants on tenants.id = catalog_analytics.tenant_id").
			Order("views_count desc").
			Scan(&rows).Error

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener analíticas"})
			return
		}

		results := make([]models.CatalogAnalyticsDTO, len(rows))
		for i, r := range rows {
			conversionRate := 0.0
			if r.ViewsCount > 0 {
				conversionRate = float64(r.OrdersGenerated) / float64(r.ViewsCount) * 100
			}

			results[i] = models.CatalogAnalyticsDTO{
				TenantID:        r.TenantID,
				BusinessName:    r.BusinessName,
				ViewsCount:      r.ViewsCount,
				OrdersGenerated: r.OrdersGenerated,
				ConversionRate:  conversionRate,
				LastViewedAt:    r.LastViewedAt,
			}
		}

		c.JSON(http.StatusOK, results)
	}
}
