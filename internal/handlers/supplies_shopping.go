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
			cost := math.Round(shortfall*sp.PricePerUnit*100) / 100
			total += cost
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
