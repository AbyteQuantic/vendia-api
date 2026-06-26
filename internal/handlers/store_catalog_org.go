// Spec: specs/082-catalogo-online-personalizacion/spec.md
package handlers

import (
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UpdateCatalogOrganization — PATCH /api/v1/store/catalog-organization
//
// Organiza el catálogo online (Spec 082 F3) en una sola llamada idempotente:
//   - category_order: nuevo orden de las categorías.
//   - hidden_ids:     IDs de productos OCULTOS (el resto del tenant = visible).
//   - featured_ids:   IDs de productos DESTACADOS (el resto = normal).
// Cada lista, si viene, REEMPLAZA el estado anterior (no acumula).
func UpdateCatalogOrganization(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		CategoryOrder []string `json:"category_order"`
		HiddenIDs     []string `json:"hidden_ids"`
		FeaturedIDs   []string `json:"featured_ids"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.CategoryOrder != nil {
			if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).
				Update("category_order", req.CategoryOrder).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el orden"})
				return
			}
		}

		// Ocultos: reinicia a visible y marca los enviados como ocultos.
		if req.HiddenIDs != nil {
			db.Model(&models.Product{}).Where("tenant_id = ?", tenantID).
				Update("hidden_in_catalog", false)
			if len(req.HiddenIDs) > 0 {
				db.Model(&models.Product{}).
					Where("tenant_id = ? AND id IN ?", tenantID, req.HiddenIDs).
					Update("hidden_in_catalog", true)
			}
		}

		// Destacados: reinicia a normal y marca los enviados como destacados.
		if req.FeaturedIDs != nil {
			db.Model(&models.Product{}).Where("tenant_id = ?", tenantID).
				Update("is_featured", false)
			if len(req.FeaturedIDs) > 0 {
				db.Model(&models.Product{}).
					Where("tenant_id = ? AND id IN ?", tenantID, req.FeaturedIDs).
					Update("is_featured", true)
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "Catálogo organizado"})
	}
}
