// Spec: specs/078-centro-tareas-unificado/spec.md (Spec 077 base)
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const recvEpsilon = 1e-9

// ReceiveErrand — POST /api/v1/errands/:id/receive
// Marca un mandado como COMPRADO (o PARCIAL) e INGRESA el inventario real: por cada
// línea ligada (insumo o producto de tienda) sube el stock, registra una COMPRA REAL
// en el kardex (purchase_receipt) y fija el costo. Spec 078 B2/B3:
//   - Body OPCIONAL {lines:[{line_id, received_qty}]} → compra PARCIAL: ingresa solo lo
//     que se compró de verdad (clamp 0..Qty). Sin body = línea completa (retrocompatible).
//   - IDEMPOTENTE por DELTA: la clave incluye el total recibido, así re-recibir lo mismo
//     no duplica, y recibir el resto ingresa solo el faltante.
//   - El mandado queda 'comprado' si TODAS las líneas se cumplieron; si no, 'parcial'
//     (se puede re-recibir el resto). NUNCA infla stock (preferible no subir a subir de más).
func ReceiveErrand(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		id := c.Param("id")

		// Body opcional con la cantidad recibida por línea (compra parcial).
		var body struct {
			Lines []struct {
				LineID      string  `json:"line_id"`
				ReceivedQty float64 `json:"received_qty"`
			} `json:"lines"`
		}
		_ = c.ShouldBindJSON(&body)
		recvByLine := make(map[string]float64, len(body.Lines))
		for _, l := range body.Lines {
			recvByLine[l.LineID] = l.ReceivedQty
		}
		hasBody := len(body.Lines) > 0

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
		allFulfilled := true
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, line := range errand.Lines {
				if line.Qty <= 0 {
					skipped++
					continue
				}
				// total que debe quedar recibido para esta línea.
				target := line.Qty // default: completa (retrocompat)
				if hasBody {
					if v, ok := recvByLine[line.ID]; ok {
						target = v
					} else {
						target = line.ReceivedQty // no vino en el body → no cambia
					}
				}
				if target < 0 {
					target = 0
				}
				// Sin tope superior: se compra por empaque entero (8 ajos para una
				// necesidad de 7.8), se registra TODO lo que llegó; el excedente queda
				// como stock. Spec 078. 'cumplida' = cubrió al menos la necesidad.
				fulfilled := target >= line.Qty-recvEpsilon
				delta := target - line.ReceivedQty

				if delta > recvEpsilon {
					ingressed, ierr := ingressLineDelta(tx, tenantID, errand.ID, line, delta, target)
					if ierr != nil {
						return ierr
					}
					if ingressed {
						received++
					} else {
						skipped++
						fulfilled = false // no se pudo ingresar (sin entidad ligada)
					}
				}
				if !fulfilled {
					allFulfilled = false
				}
				// Persiste el avance de la línea (recibido + cumplida).
				if err := tx.Model(&models.PurchaseErrandLine{}).
					Where("id = ?", line.ID).
					Updates(map[string]any{"received_qty": target, "fulfilled": fulfilled}).Error; err != nil {
					return err
				}
			}

			status := "parcial"
			updates := map[string]any{"status": status}
			if allFulfilled {
				status = "comprado"
				now := time.Now()
				updates["status"] = status
				updates["closed_at"] = &now
			}
			return tx.Model(&models.PurchaseErrand{}).
				Where("id = ? AND tenant_id = ?", errand.ID, tenantID).
				Updates(updates).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo ingresar el inventario"})
			return
		}
		status := "parcial"
		if allFulfilled {
			status = "comprado"
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"received": received, "skipped": skipped, "status": status,
			"message": "Inventario ingresado",
		}})
	}
}

// ingressLineDelta ingresa el DELTA de una línea (insumo o producto) al kardex +
// stock, idempotente por la clave errand:{id}:{line}:{target}. Devuelve (ingresó,
// error). ingresó=false si la línea no tiene entidad ligada o el delta ya existía.
func ingressLineDelta(tx *gorm.DB, tenantID, errandID string, line models.PurchaseErrandLine, delta, target float64) (bool, error) {
	key := fmt.Sprintf("errand:%s:%s:%g", errandID, line.ID, target)
	ref := errandID

	// PRODUCTO DE TIENDA: stock entero, el kardex auto-lee before/after de products.
	if line.LineKind == "product" && line.ProductID != nil && *line.ProductID != "" {
		var prod models.Product
		if err := tx.Where("id = ? AND tenant_id = ?", *line.ProductID, tenantID).First(&prod).Error; err != nil {
			return false, nil
		}
		qtyInt := int(delta + recvEpsilon)
		if qtyInt <= 0 {
			return false, nil
		}
		mErr := services.LogInventoryMovement(tx, services.MovementParams{
			TenantID: tenantID, ProductID: prod.ID, ProductName: prod.Name,
			MovementType: models.MovementPurchaseReceipt, Quantity: qtyInt,
			IdempotencyKey: &key, ReferenceType: "errand", ReferenceID: &ref,
			Notes: "compra de mandado",
		})
		if mErr != nil {
			if errors.Is(mErr, services.ErrDuplicateMovement) {
				return false, nil
			}
			return false, mErr
		}
		if err := tx.Model(&models.Product{}).Where("id = ? AND tenant_id = ?", prod.ID, tenantID).
			UpdateColumn("stock", gorm.Expr("stock + ?", qtyInt)).Error; err != nil {
			return false, err
		}
		if line.EstimatedUnitPrice > 0 && line.EstimatedUnitPrice != prod.PurchasePrice {
			tx.Model(&models.Product{}).Where("id = ? AND tenant_id = ?", prod.ID, tenantID).
				UpdateColumn("purchase_price", line.EstimatedUnitPrice)
		}
		return true, nil
	}

	// INSUMO: stock fraccionable en ingredients (no products) → overrides explícitos.
	if line.IngredientID == nil || *line.IngredientID == "" {
		return false, nil
	}
	var ing models.Ingredient
	if err := tx.Where("id = ? AND tenant_id = ?", *line.IngredientID, tenantID).First(&ing).Error; err != nil {
		return false, nil
	}
	before := ing.Stock
	after := before + delta
	mErr := services.LogInventoryMovement(tx, services.MovementParams{
		TenantID: tenantID, ProductID: ing.ID, ProductName: ing.Name,
		MovementType: models.MovementPurchaseReceipt, Quantity: int(delta),
		QuantityOverride: &delta, StockBeforeOverride: &before, StockAfterOverride: &after,
		IdempotencyKey: &key, ReferenceType: "errand", ReferenceID: &ref,
		Notes: "compra de mandado",
	})
	if mErr != nil {
		if errors.Is(mErr, services.ErrDuplicateMovement) {
			return false, nil
		}
		return false, mErr
	}
	if err := tx.Model(&models.Ingredient{}).Where("id = ? AND tenant_id = ?", ing.ID, tenantID).
		UpdateColumn("stock", gorm.Expr("stock + ?", delta)).Error; err != nil {
		return false, err
	}
	if line.EstimatedUnitPrice > 0 && line.EstimatedUnitPrice != ing.UnitCost {
		if err := tx.Model(&models.Ingredient{}).Where("id = ? AND tenant_id = ?", ing.ID, tenantID).
			UpdateColumn("unit_cost", line.EstimatedUnitPrice).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}
