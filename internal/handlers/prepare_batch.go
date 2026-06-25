// Spec: specs/080-platos-por-porciones/spec.md
package handlers

import (
	"net/http"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PrepareDishBatch — POST /api/v1/products/:id/prepare-batch  {portions:N}
//
// Cocina un LOTE de un plato "por porciones": descuenta los insumos de N
// porciones UNA sola vez (explota la receta con ForPrep), fija `stock = N`
// (porciones disponibles hoy), marca el plato como por_porciones y `prepared_date
// = hoy`. La venta posterior descuenta `stock` SIN re-explotar insumos
// (gate en ApplyPostSale/ExplodeRecipe) → cero doble descuento (Spec 052/080).
//
// Idempotencia: el anchor de explosión es "batch:{producto}:{hoy}", así un
// doble-toque el mismo día NO descuenta insumos dos veces. `stock` se FIJA a N.
func PrepareDishBatch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productID := c.Param("id")

		var req struct {
			Portions int `json:"portions" binding:"required,gt=0"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "porciones inválidas"})
			return
		}

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ? AND is_menu_item = ?",
			productID, tenantID, true).First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "plato no encontrado"})
			return
		}
		// Sólo platos COMPLETOS (con receta) se pueden cocinar por lote: el
		// descuento de insumos necesita la receta. Un incompleto se completa
		// primero en "Mis recetas".
		if !product.IsRecipe || product.RecipeID == nil || *product.RecipeID == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "complete la receta del plato antes de cocinar por porciones"})
			return
		}

		today := time.Now().Format("2006-01-02")
		recipeSvc := services.NewRecipeService(db)

		if err := db.Transaction(func(tx *gorm.DB) error {
			// Descuenta insumos de N porciones (una vez por día, idempotente).
			if err := recipeSvc.ExplodeRecipe(tx, services.ExplodeParams{
				TenantID:  tenantID,
				SaleUUID:  "batch:" + productID + ":" + today,
				ProductID: productID,
				Quantity:  req.Portions,
				BranchID:  product.BranchID,
				UserID:    middleware.GetUserIDPtr(c),
				ForPrep:   true,
			}); err != nil {
				return err
			}
			return tx.Model(&models.Product{}).
				Where("id = ? AND tenant_id = ?", productID, tenantID).
				Updates(map[string]any{
					"availability_mode": "por_porciones",
					"stock":             req.Portions,
					"prepared_date":     today,
				}).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el lote"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"id": productID, "availability_mode": "por_porciones",
			"stock": req.Portions, "prepared_date": today,
		}})
	}
}
