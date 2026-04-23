package handlers

import (
	"log"
	"net/http"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// dbErrorResponse wraps a DB error into a JSON body that keeps the
// human-readable message in "error" (what the UI surfaces) but also
// includes the raw driver message in "detail".
//
// Before this refactor every CMS handler returned a generic
// `{"error":"error al listar plantillas"}` regardless of what blew up
// under the hood. When deployments hit a missing table ("relation
// catalog_templates does not exist") or a column mismatch, Ops had no
// way to diagnose from the browser — the real error lived in the
// Render logs. We now echo it back so the frontend (and curl) can see
// it directly. This is safe because the Go handlers never embed
// secrets in DB errors, and the endpoints are behind SuperAdminOnly.
func dbErrorResponse(c *gin.Context, route, userMsg string, err error) {
	log.Printf("[CMS] %s failed: %v", route, err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"error":  userMsg,
		"detail": err.Error(),
	})
}

func AdminListCatalogTemplates(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var templates []models.CatalogTemplate
		if err := db.Order("created_at desc").Find(&templates).Error; err != nil {
			dbErrorResponse(c, "list catalog templates", "error al listar plantillas", err)
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
			dbErrorResponse(c, "create catalog template", "error al crear plantilla", err)
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
			dbErrorResponse(c, "update catalog template", "error al actualizar plantilla", err)
			return
		}
		c.JSON(http.StatusOK, template)
	}
}

func AdminDeleteCatalogTemplate(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if err := db.Delete(&models.CatalogTemplate{}, "id = ?", id).Error; err != nil {
			dbErrorResponse(c, "delete catalog template", "error al eliminar plantilla", err)
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
			dbErrorResponse(c, "list catalog analytics", "error al obtener analíticas", err)
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
