// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import (
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// SuggestedPrice — el mejor precio conocido de un insumo, con su ORIGEN visible.
// El sugerido NO se persiste: se calcula en lectura por prioridad de fuente.
type SuggestedPrice struct {
	PricePerUnit float64 `json:"price_per_unit"`
	Source       string  `json:"source"`      // vendia_catalog|manual|invoice_ocr|scraped_chain|ultima_compra|ninguno
	Confidence   float64 `json:"confidence"`  // 0-1
	IsEstimate   bool    `json:"is_estimate"` // true → mostrar badge "Estimado"
	SupplierName string  `json:"supplier_name,omitempty"`

	// Datos del EMPAQUE para el costo empaque-completo (Spec 077). PackUnknown
	// true → no se conoce el empaque; el costo cae a per-unidad (aproximado).
	PackPrice   float64 `json:"pack_price"`   // precio del empaque completo
	PackQty     float64 `json:"pack_qty"`     // tamaño del empaque en unidad base
	PackUnit    string  `json:"pack_unit"`    // unidad del empaque
	PackLabel   string  `json:"pack_label"`   // presentación (ej "Crema de leche x 1L")
	PackUnknown bool    `json:"pack_unknown"` // true → sin empaque conocido
}

// sourceRank — prioridad descendente (mayor = más confiable). El sugerido toma
// la fuente de mayor rango y, dentro de ella, la captura más reciente.
var sourceRank = map[string]int{
	models.PriceSourceVendiaCatalog: 40,
	models.PriceSourceManual:        30,
	models.PriceSourceInvoiceOCR:    20,
	models.PriceSourceScrapedChain:  10,
}

// fuentes garantizadas (no estimadas): catálogo VendIA y precio manual del propio
// tenant. Las demás (factura vieja, scraping) son ESTIMADO.
func isEstimateSource(s string) bool {
	return s != models.PriceSourceVendiaCatalog && s != models.PriceSourceManual
}

// SuggestPrice devuelve el mejor precio para un insumo: revisa ingredient_prices
// (todas las fuentes) y cae a la última compra (Ingredient.UnitCost) si no hay
// nada. branchID ” = comparte entre sedes; con sede, prioriza esa sede.
func SuggestIngredientPrice(db *gorm.DB, tenantID, branchID, ingredientID string, lastPurchaseCost float64) SuggestedPrice {
	var rows []models.IngredientPrice
	q := db.Where("tenant_id = ? AND ingredient_id = ?", tenantID, ingredientID)
	if branchID != "" {
		q = q.Where("branch_id = ? OR branch_id = ''", branchID)
	}
	q.Find(&rows)

	var best *models.IngredientPrice
	bestRank := -1
	for i := range rows {
		r := rows[i]
		rank := sourceRank[r.Source]
		if rank > bestRank ||
			(rank == bestRank && best != nil && r.CapturedAt.After(best.CapturedAt)) {
			best = &rows[i]
			bestRank = rank
		}
	}

	if best != nil {
		price := best.PricePerBaseUnit
		if price <= 0 {
			price = best.UnitPrice
		}
		return SuggestedPrice{
			PricePerUnit: price,
			Source:       best.Source,
			Confidence:   best.Confidence,
			IsEstimate:   isEstimateSource(best.Source),
			SupplierName: best.SupplierName,
			PackPrice:    best.UnitPrice,
			PackQty:      best.PackQty,
			PackUnit:     best.PackUnit,
			PackLabel:    best.RawName,
			PackUnknown:  best.PackQty <= 0 || best.UnitPrice <= 0,
		}
	}

	// Última compra: SOLO si hubo una COMPRA REAL registrada (kardex). El
	// unit_cost del insumo sin compra es solo el valor que se puso al costear la
	// receta — no un precio de compra, así que NO se presenta como "tu costo".
	if lastPurchaseCost > 0 && HasRealPurchase(db, tenantID, ingredientID) {
		return SuggestedPrice{
			PricePerUnit: lastPurchaseCost,
			Source:       "ultima_compra",
			Confidence:   0.5,
			IsEstimate:   true,
			PackUnknown:  true,
		}
	}
	return SuggestedPrice{Source: "ninguno", IsEstimate: true, PackUnknown: true}
}

// HasRealPurchase indica si el insumo tuvo una COMPRA registrada de verdad
// (movimiento de kardex 'purchase_receipt'). Distingue una compra real del
// unit_cost que el tendero/IA puso al costear la receta (Spec 077).
func HasRealPurchase(db *gorm.DB, tenantID, ingredientID string) bool {
	if ingredientID == "" {
		return false
	}
	var n int64
	db.Model(&models.InventoryMovement{}).
		Where("tenant_id = ? AND product_id = ? AND movement_type = ?",
			tenantID, ingredientID, models.MovementPurchaseReceipt).
		Count(&n)
	return n > 0
}
