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
		}
	}

	if lastPurchaseCost > 0 {
		return SuggestedPrice{
			PricePerUnit: lastPurchaseCost,
			Source:       "ultima_compra",
			Confidence:   0.5,
			IsEstimate:   true,
		}
	}
	return SuggestedPrice{Source: "ninguno", IsEstimate: true}
}
