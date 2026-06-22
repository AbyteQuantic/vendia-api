// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers

import (
	"math"
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type shoppingNeed struct {
	IngredientID string  `json:"ingredient_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Qty          float64 `json:"qty"`
}

type ShoppingItem struct {
	IngredientID  string  `json:"ingredient_id"`
	Name          string  `json:"name"`
	Unit          string  `json:"unit"`
	Needed        float64 `json:"needed"`
	Stock         float64 `json:"stock"`
	Shortfall     float64 `json:"shortfall"`
	PricePerUnit  float64 `json:"price_per_unit"`
	EstimatedCost float64 `json:"estimated_cost"`
	PriceSource   string  `json:"price_source"`
	IsEstimate    bool    `json:"is_estimate"`

	// Empaque-completo (Spec 077): nadie vende fracciones; se compra el/los
	// empaque(s) entero(s) y queda un sobrante reservado.
	Packs       *int    `json:"packs,omitempty"`      // nº de empaques (nil si no se conoce el empaque)
	PackLabel   string  `json:"pack_label,omitempty"` // presentación (ej "Crema de leche x 1L")
	PackUnit    string  `json:"pack_unit,omitempty"`  // unidad del empaque
	Leftover    float64 `json:"leftover"`             // sobrante reservado (estimado)
	PackUnknown bool    `json:"pack_unknown"`         // true → costo aproximado per-unidad
}

// SuppliesShoppingList — POST /api/v1/supplies/shopping-list
// Fase 1 (Spec 077): recibe lo que el menú NECESITA (calculado en vivo en la
// pantalla Alistar del día), resta el stock actual de cada insumo y devuelve LO
// QUE FALTA comprar + precio sugerido (multi-fuente, hoy = última compra) +
// costo estimado. Read-only. El precio es SUGERENCIA con su origen visible.
func SuppliesShoppingList(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req struct {
			Needs    []shoppingNeed `json:"needs"`
			BranchID string         `json:"branch_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "datos inválidos"})
			return
		}

		// DEDUP por insumo: una receta con líneas duplicadas (o un insumo en
		// varios platos) llega como varios needs del MISMO ingredient_id → suma
		// las cantidades una sola vez (evita el costo doble). Spec 077 fix.
		dedup := make([]shoppingNeed, 0, len(req.Needs))
		idx := map[string]int{}
		for _, n := range req.Needs {
			if i, ok := idx[n.IngredientID]; ok {
				dedup[i].Qty += n.Qty
				continue
			}
			idx[n.IngredientID] = len(dedup)
			dedup = append(dedup, n)
		}
		req.Needs = dedup

		// Carga los insumos referenciados (stock + costo) en una query.
		ids := make([]string, 0, len(req.Needs))
		for _, n := range req.Needs {
			if n.IngredientID != "" {
				ids = append(ids, n.IngredientID)
			}
		}
		ingByID := map[string]models.Ingredient{}
		if len(ids) > 0 {
			var ings []models.Ingredient
			db.Where("tenant_id = ? AND id IN ?", tenantID, ids).Find(&ings)
			for _, g := range ings {
				ingByID[g.ID] = g
			}
		}

		// Ciudad del tenant para el fallback de scraping (precios por ciudad/nacional).
		var tenant models.Tenant
		db.Select("city").Where("id = ?", tenantID).First(&tenant)

		items := make([]ShoppingItem, 0, len(req.Needs))
		var total float64
		hasEstimate := false
		for _, n := range req.Needs {
			g := ingByID[n.IngredientID]
			shortfall := n.Qty - g.Stock
			if shortfall <= 0 {
				continue // ya tiene suficiente
			}
			sp := services.SuggestIngredientPrice(db, tenantID, req.BranchID, n.IngredientID, g.UnitCost)

			// FALLBACK DE SCRAPING (#1): sin compra previa ni precio de proveedor,
			// en vez de "sin precio" sugiere lo que cuesta en las cadenas.
			if sp.Source == "ninguno" {
				if m := services.BestChainPrice(db, services.NormalizeText(n.Name), tenant.City); m != nil {
					ppu := m.PricePerBaseUnit
					if ppu <= 0 {
						ppu = m.Price
					}
					sp = services.SuggestedPrice{
						PricePerUnit: ppu, Source: models.PriceSourceScrapedChain,
						IsEstimate: true, SupplierName: m.Chain,
						PackPrice: m.Price, PackQty: m.PackQty, PackUnit: m.Unit,
						PackLabel: m.RawName, PackUnknown: m.PackQty <= 0,
					}
				}
			}

			// COSTO EMPAQUE-COMPLETO: si se conoce el empaque (precio + tamaño en
			// la unidad del insumo), se compra el/los empaque(s) entero(s) y queda
			// un sobrante reservado. Si no, cae a per-unidad (aproximado).
			var cost, leftover float64
			var packsPtr *int
			packUnknown := true
			// Convierte faltante y empaque a la MISMA base (kg↔g, l↔ml) — un
			// insumo en kg con un empaque en g debe cuadrar (Spec 077).
			sBase, sDim := services.ToBaseQty(shortfall, g.Unit)
			pBase, pDim := services.ToBaseQty(sp.PackQty, sp.PackUnit)
			if !sp.PackUnknown && pBase > 0 && sDim == pDim && sDim != "other" {
				pc := services.ComputePackagingCost(sBase, pBase, sp.PackPrice)
				if pc.PackagingKnown {
					cost = pc.Cost
					leftover = pc.Leftover
					p := pc.Packs
					packsPtr = &p
					packUnknown = false
				}
			}
			if packUnknown {
				// per-unidad en la unidad del insumo (sp.PricePerUnit es por esa unidad).
				cost = math.Round(shortfall*sp.PricePerUnit*100) / 100
			}
			total += cost
			// "Estimado" = confianza del PRECIO (origen). El empaque desconocido
			// es una señal aparte (PackUnknown) que la fila rotula "aproximado".
			if sp.IsEstimate {
				hasEstimate = true
			}
			items = append(items, ShoppingItem{
				IngredientID:  n.IngredientID,
				Name:          n.Name,
				Unit:          n.Unit,
				Needed:        n.Qty,
				Stock:         g.Stock,
				Shortfall:     math.Round(shortfall*100) / 100,
				PricePerUnit:  sp.PricePerUnit,
				EstimatedCost: cost,
				PriceSource:   sp.Source,
				IsEstimate:    sp.IsEstimate,
				Packs:         packsPtr,
				PackLabel:     sp.PackLabel,
				PackUnit:      sp.PackUnit,
				Leftover:      leftover,
				PackUnknown:   packUnknown,
			})
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"items":           items,
			"total_estimated": math.Round(total*100) / 100,
			"has_estimate":    hasEstimate,
			"disclaimer":      "Los precios marcados “Estimado” son cálculos sobre su última compra, facturas o catálogos en línea; pueden variar. Confirme con su proveedor.",
		}})
	}
}
