// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers

import (
	"math"
	"net/http"
	"strconv"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func roundCents(v float64) float64 { return math.Round(v*100) / 100 }

// PriceOption — una opción de COMPRA de un insumo (un proveedor o una cadena),
// con el costo empaque-completo ya resuelto contra el faltante (Spec 077).
type PriceOption struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`    // presentación (ej "Crema de leche x 1L")
	Supplier    string  `json:"supplier"` // proveedor propio o cadena
	Source      string  `json:"source"`
	PackPrice   float64 `json:"pack_price"`
	PackQty     float64 `json:"pack_qty"`
	PackUnit    string  `json:"pack_unit"`
	Packs       *int    `json:"packs,omitempty"`
	Cost        float64 `json:"cost"`
	Leftover    float64 `json:"leftover"`
	PackUnknown bool    `json:"pack_unknown"`

	PricePerBaseUnit float64 `json:"price_per_base_unit"`
	IsEstimate       bool    `json:"is_estimate"`
	Recommended      bool    `json:"recommended"`
	Dropped          bool    `json:"dropped,omitempty"`
	DropPct          float64 `json:"drop_pct,omitempty"`
}

func optionIsEstimate(source string) bool {
	return source != models.PriceSourceVendiaCatalog && source != models.PriceSourceManual
}

// SupplyOptions — GET /api/v1/supplies/options?ingredient_id=&name=&unit=&shortfall=
// Devuelve TODAS las opciones de compra de un insumo (mis proveedores + cadenas +
// última compra), cada una con el costo empaque-completo resuelto. La default
// recomendada = menor precio por unidad base entre las NO estimadas.
func SupplyOptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		ingredientID := c.Query("ingredient_id")
		name := c.Query("name")
		unit := c.Query("unit")
		shortfall, _ := strconv.ParseFloat(c.Query("shortfall"), 64)
		if shortfall <= 0 {
			shortfall = 1
		}

		opts := make([]PriceOption, 0, 8)

		// 1) MIS PROVEEDORES: ingredient_prices del tenant para este insumo.
		if ingredientID != "" {
			var rows []models.IngredientPrice
			db.Where("tenant_id = ? AND ingredient_id = ?", tenantID, ingredientID).Find(&rows)
			for _, r := range rows {
				supplier := r.SupplierName
				if supplier == "" {
					supplier = "Mi precio"
				}
				o := PriceOption{
					ID:               "ip:" + r.ID,
					Label:            firstNonEmpty(r.RawName, name),
					Supplier:         supplier,
					Source:           r.Source,
					PackPrice:        r.UnitPrice,
					PackQty:          r.PackQty,
					PackUnit:         r.PackUnit,
					PricePerBaseUnit: r.PricePerBaseUnit,
					IsEstimate:       optionIsEstimate(r.Source),
				}
				applyPackaging(&o, shortfall, unit)
				opts = append(opts, o)
			}
		}

		// 2) CADENAS: por la ciudad del tenant, match por nombre normalizado.
		var tenant models.Tenant
		db.Select("city").Where("id = ?", tenantID).First(&tenant)
		if name != "" {
			for _, m := range services.MatchChainPrices(db, services.NormalizeText(name), tenant.City) {
				o := PriceOption{
					ID:               "chain:" + m.Chain + ":" + m.RawName,
					Label:            m.RawName,
					Supplier:         m.Chain,
					Source:           models.PriceSourceScrapedChain,
					PackPrice:        m.Price,
					PackQty:          m.PackQty,
					PackUnit:         m.Unit,
					PricePerBaseUnit: m.PricePerBaseUnit,
					IsEstimate:       true,
					Dropped:          m.Dropped,
					DropPct:          m.DropPct,
				}
				applyPackaging(&o, shortfall, unit)
				opts = append(opts, o)
			}
		}

		// 3) ÚLTIMA COMPRA — SOLO si hubo una compra REAL (kardex). Sin compra, el
		// unit_cost es solo el valor de costeo de receta, no un precio: no se muestra.
		if ingredientID != "" && services.HasRealPurchase(db, tenantID, ingredientID) {
			var ing models.Ingredient
			if err := db.Select("unit_cost").Where("tenant_id = ? AND id = ?", tenantID, ingredientID).First(&ing).Error; err == nil && ing.UnitCost > 0 {
				o := PriceOption{
					ID: "last", Label: "Última compra", Supplier: "Última compra",
					Source: "ultima_compra", PricePerBaseUnit: ing.UnitCost,
					IsEstimate: true, PackUnknown: true,
				}
				applyPackaging(&o, shortfall, unit)
				opts = append(opts, o)
			}
		}

		markRecommended(opts)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"options": opts}})
	}
}

// applyPackaging resuelve packs/cost/leftover de una opción contra el faltante.
// Convierte faltante y empaque a la MISMA unidad base (kg↔g, l↔ml) para que el
// costo cuadre aunque el insumo esté en kg y la cadena en g (Spec 077).
func applyPackaging(o *PriceOption, shortfall float64, ingredientUnit string) {
	sBase, sDim := services.ToBaseQty(shortfall, ingredientUnit)
	pBase, pDim := services.ToBaseQty(o.PackQty, o.PackUnit)
	if pBase > 0 && o.PackPrice > 0 && sDim == pDim && sDim != "other" {
		pc := services.ComputePackagingCost(sBase, pBase, o.PackPrice)
		if pc.PackagingKnown {
			p := pc.Packs
			o.Packs = &p
			o.Cost = pc.Cost
			o.Leftover = pc.Leftover // en unidad base (g/ml)
			if o.PricePerBaseUnit <= 0 {
				o.PricePerBaseUnit = o.PackPrice / pBase
			}
			return
		}
	}
	// Fallback aproximado: no se conoce un empaque comparable.
	o.PackUnknown = true
	if o.PackPrice > 0 {
		// Hay precio de empaque (cadena/proveedor) pero su unidad no convierte a
		// la del insumo → mostramos el empaque entero (no mezclamos unidades).
		o.Cost = roundCents(o.PackPrice)
	} else {
		// Última compra: PricePerBaseUnit está en la unidad DEL INSUMO (mismo que
		// el faltante) → costo = faltante × costo unitario.
		o.Cost = roundCents(shortfall * o.PricePerBaseUnit)
	}
}

// markRecommended elige la opción a recomendar. NUNCA recomienda "tu costo
// estimado" (ultima_compra) si hay un precio REAL de mercado (proveedor/cadena):
// ese es solo el valor que el tendero puso al crear el insumo, no una compra.
//  1. Entre las NO estimadas (catálogo VendIA / precio manual): menor precio por
//     unidad base.
//  2. Si no hay, entre las de MERCADO (proveedor/cadena, excluye tu-costo): menor costo.
//  3. Solo si "tu costo" es la ÚNICA opción, se recomienda (no hay nada mejor).
func markRecommended(opts []PriceOption) {
	best := -1
	for i := range opts {
		if opts[i].IsEstimate {
			continue
		}
		if best == -1 || opts[i].PricePerBaseUnit < opts[best].PricePerBaseUnit {
			best = i
		}
	}
	if best == -1 { // sin garantizadas → mejor opción de MERCADO (no tu-costo)
		for i := range opts {
			if opts[i].Source == "ultima_compra" {
				continue
			}
			if best == -1 || opts[i].Cost < opts[best].Cost {
				best = i
			}
		}
	}
	if best == -1 { // tu-costo es lo único que hay
		for i := range opts {
			if best == -1 || opts[i].Cost < opts[best].Cost {
				best = i
			}
		}
	}
	if best >= 0 {
		opts[best].Recommended = true
	}
}
