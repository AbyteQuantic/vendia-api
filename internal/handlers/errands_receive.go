// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers

import (
	"errors"
	"net/http"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ReceiveErrand — POST /api/v1/errands/:id/receive
// Marca un mandado como COMPRADO e INGRESA el inventario al sistema: por cada
// línea ligada a un insumo, sube el stock, registra una COMPRA REAL en el kardex
// (purchase_receipt) y actualiza el costo. Idempotente: si ya está comprado o el
// movimiento ya existe, no duplica. Esto convierte "marqué que ya compré" en
// stock + costo reales (y habilita "última compra" del Spec 078). Spec 077.
func ReceiveErrand(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		id := c.Param("id")

		var errand models.PurchaseErrand
		if err := db.Preload("Lines").Where("id = ? AND tenant_id = ?", id, tenantID).First(&errand).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "mandado no encontrado"})
			return
		}
		if errand.Status == "comprado" || errand.Status == "cancelado" {
			c.JSON(http.StatusOK, gin.H{"data": gin.H{"received": 0, "status": errand.Status, "already": true}})
			return
		}

		received, skipped := 0, 0
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, line := range errand.Lines {
				if line.IngredientID == nil || *line.IngredientID == "" || line.Qty <= 0 {
					skipped++ // línea sin insumo ligado (texto libre) — no se ingresa
					continue
				}
				var ing models.Ingredient
				if err := tx.Where("id = ? AND tenant_id = ?", *line.IngredientID, tenantID).First(&ing).Error; err != nil {
					skipped++
					continue
				}
				before := ing.Stock
				after := before + line.Qty
				qty := line.Qty
				key := "errand:" + errand.ID + ":" + line.ID
				ref := errand.ID
				mErr := services.LogInventoryMovement(tx, services.MovementParams{
					TenantID: tenantID, ProductID: ing.ID, ProductName: ing.Name,
					MovementType:        models.MovementPurchaseReceipt,
					Quantity:            int(qty),
					QuantityOverride:    &qty,
					StockBeforeOverride: &before,
					StockAfterOverride:  &after,
					IdempotencyKey:      &key,
					ReferenceType:       "errand",
					ReferenceID:         &ref,
					Notes:               "compra de mandado",
				})
				if mErr != nil {
					if errors.Is(mErr, services.ErrDuplicateMovement) {
						skipped++
						continue // ya se ingresó esta línea antes
					}
					return mErr
				}
				if err := tx.Model(&models.Ingredient{}).
					Where("id = ? AND tenant_id = ?", ing.ID, tenantID).
					UpdateColumn("stock", gorm.Expr("stock + ?", qty)).Error; err != nil {
					return err
				}
				// El costo real de la compra pasa a ser el costo del insumo (última compra).
				if line.EstimatedUnitPrice > 0 && line.EstimatedUnitPrice != ing.UnitCost {
					if err := tx.Model(&models.Ingredient{}).
						Where("id = ? AND tenant_id = ?", ing.ID, tenantID).
						UpdateColumn("unit_cost", line.EstimatedUnitPrice).Error; err != nil {
						return err
					}
				}
				received++
			}
			now := time.Now()
			return tx.Model(&models.PurchaseErrand{}).
				Where("id = ? AND tenant_id = ?", errand.ID, tenantID).
				Updates(map[string]any{"status": "comprado", "closed_at": &now}).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo ingresar el inventario"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"received": received, "skipped": skipped, "status": "comprado",
			"message": "Inventario ingresado",
		}})
	}
}
