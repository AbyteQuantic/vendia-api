// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers

import (
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ProductReorderList — GET /api/v1/products/reorder-list
// Espejo de la lista de compra pero para PRODUCTOS DE TIENDA en/bajo su mínimo
// (se compra el producto mismo, no insumos). Devuelve líneas line_kind='product'
// que el mismo mandado/ingreso de inventario puede recibir. Spec 078 B2.
// Scope por sede (productos son por sede) + globales (OR branch_id IS NULL).
func ProductReorderList(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{"error": "branch_not_owned"})
			return
		}

		q := db.Where("tenant_id = ? AND min_stock > 0 AND stock <= min_stock AND is_available = ?", tenantID, true)
		q = ApplyBranchScope(q, scope)
		var products []models.Product
		q.Order("name ASC").Find(&products)

		items := make([]gin.H, 0, len(products))
		var total float64
		for _, p := range products {
			shortfall := p.MinStock - p.Stock
			if shortfall <= 0 {
				continue
			}
			cost := float64(shortfall) * p.PurchasePrice
			total += cost
			items = append(items, gin.H{
				"product_uuid": p.ID, "line_kind": "product", "name": p.Name,
				"unit": "unidad", "shortfall": shortfall, "stock": p.Stock, "min_stock": p.MinStock,
				"unit_price": p.PurchasePrice, "estimated_cost": cost,
				"is_estimate": p.PurchasePrice <= 0,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"items": items, "total_estimated": total,
		}})
	}
}
