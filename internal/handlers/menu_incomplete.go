// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers

import (
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// IncompleteMenuItems — GET /api/v1/menu/incomplete
// Platos de menú (is_menu_item) que NO tienen una receta con ingredientes →
// están INCOMPLETOS (no se pueden costear). Importar una carta los crea así; esta
// lista alimenta el badge "Incompleto" + la alerta en "Mis recetas". Spec 078.
func IncompleteMenuItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var completeIDs []string
		db.Table("recipe_ingredients ri").
			Joins("JOIN recipes r ON r.id = ri.recipe_uuid").
			// ri.deleted_at IS NULL: el Table crudo no aplica el soft-delete de
			// GORM. Sin esto un insumo borrado contaría el plato como completo.
			Where("r.tenant_id = ? AND r.product_id IS NOT NULL AND r.deleted_at IS NULL AND ri.deleted_at IS NULL", tenantID).
			Distinct().Pluck("r.product_id", &completeIDs)

		q := db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_menu_item = ?", tenantID, true)
		if len(completeIDs) > 0 {
			q = q.Where("id NOT IN ?", completeIDs)
		}
		var products []models.Product
		q.Order("created_at DESC").Find(&products)

		out := make([]gin.H, 0, len(products))
		for _, p := range products {
			out = append(out, gin.H{
				"id": p.ID, "name": p.Name, "price": p.Price,
				"emoji": p.Emoji, "photo_url": p.PhotoURL, "description": p.Description,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}
