// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SupplySearch — GET /api/v1/supplies/search?q=&unit=&shortfall=
// Buscador libre: encuentra productos en el catálogo scrapeado de cadenas + las
// compras previas del tenant, con el costo resuelto contra el faltante. Permite
// al tendero CAMBIAR una sugerencia o armar su lista a mano, más dinámico (Spec 077, #3).
func SupplySearch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		query := strings.TrimSpace(c.Query("q"))
		if len([]rune(query)) < 2 {
			c.JSON(http.StatusOK, gin.H{"data": gin.H{"options": []PriceOption{}}})
			return
		}
		unit := c.Query("unit")
		shortfall, _ := strconv.ParseFloat(c.Query("shortfall"), 64)
		if shortfall <= 0 {
			shortfall = 1
		}

		opts := make([]PriceOption, 0, 16)

		// 1) COMPRAS PREVIAS del tenant (ingredient_prices) que matcheen el texto.
		var rows []models.IngredientPrice
		db.Where("tenant_id = ? AND raw_name LIKE ?", tenantID, "%"+query+"%").Limit(8).Find(&rows)
		for _, r := range rows {
			supplier := r.SupplierName
			if supplier == "" {
				supplier = "Compra previa"
			}
			o := PriceOption{
				ID: "ip:" + r.ID, Label: firstNonEmpty(r.RawName, query), Supplier: supplier,
				Source: r.Source, PackPrice: r.UnitPrice, PackQty: r.PackQty, PackUnit: r.PackUnit,
				PricePerBaseUnit: r.PricePerBaseUnit, IsEstimate: optionIsEstimate(r.Source),
			}
			applyPackaging(&o, shortfall, unit)
			opts = append(opts, o)
		}

		// 2) CADENAS: catálogo scrapeado, relevante y barato.
		var tenant models.Tenant
		db.Select("city").Where("id = ?", tenantID).First(&tenant)
		for _, m := range services.SearchChainProducts(db, services.NormalizeText(query), tenant.City, 12) {
			o := PriceOption{
				ID: "chain:" + m.Chain + ":" + m.RawName, Label: m.RawName, Supplier: m.Chain,
				Source: models.PriceSourceScrapedChain, PackPrice: m.Price, PackQty: m.PackQty,
				PackUnit: m.Unit, PricePerBaseUnit: m.PricePerBaseUnit, IsEstimate: true,
			}
			applyPackaging(&o, shortfall, unit)
			opts = append(opts, o)
		}

		markRecommended(opts)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"options": opts}})
	}
}
