// Spec: specs/059-admin-tenant-activity/spec.md
package handlers

import (
	"net/http"
	"sort"
	"time"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AdminTenantActivity expone el uso REAL de la plataforma de un tenant
// para el panel god-mode: logo, link del catálogo público, catálogo +
// stock, y por cada referencia su frecuencia de venta, ingresos y
// ganancia estimada. Reemplaza al panel de "Overrides de módulos" que no
// tenía sentido en el detalle del tenant.
func AdminTenantActivity(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := c.Param("id")

		var tenant models.Tenant
		if err := db.First(&tenant, "id = ?", tenantID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant no encontrado"})
			return
		}

		// Toda tienda debe tener link de catálogo por defecto. Si el slug
		// nunca se provisionó (el tendero no abrió su config de catálogo),
		// lo generamos y persistimos acá — igual que GetStoreSlug.
		if tenant.StoreSlug == nil || *tenant.StoreSlug == "" {
			if generated, err := services.GenerateUniqueSlug(db, tenant.BusinessName, tenant.ID); err == nil {
				if err := db.Model(&models.Tenant{}).
					Where("id = ?", tenant.ID).
					Update("store_slug", generated).Error; err == nil {
					tenant.StoreSlug = &generated
				}
			}
		}

		var products []models.Product
		db.Where("tenant_id = ?", tenantID).Find(&products)

		// Ventas agregadas por producto (frecuencia + ingresos). Una sola
		// query con JOIN — sin N+1.
		type soldRow struct {
			ProductID string
			Qty       int
			Revenue   float64
		}
		var sold []soldRow
		db.Table("sale_items si").
			Select("si.product_id AS product_id, SUM(si.quantity) AS qty, SUM(si.subtotal) AS revenue").
			Joins("JOIN sales s ON s.id = si.sale_id").
			Where("s.tenant_id = ? AND s.deleted_at IS NULL AND si.deleted_at IS NULL AND si.product_id IS NOT NULL", tenantID).
			Group("si.product_id").
			Scan(&sold)
		soldByID := make(map[string]soldRow, len(sold))
		for _, r := range sold {
			soldByID[r.ProductID] = r
		}

		type productRow struct {
			ID              string  `json:"id"`
			Name            string  `json:"name"`
			Stock           int     `json:"stock"`
			MinStock        int     `json:"min_stock"`
			Price           float64 `json:"price"`
			PurchasePrice   float64 `json:"purchase_price"`
			UnitsSold       int     `json:"units_sold"`
			Revenue         float64 `json:"revenue"`
			Profit          float64 `json:"profit"`
			ProfitKnown     bool    `json:"profit_known"`
			IngestionMethod string  `json:"ingestion_method"`
		}

		rows := make([]productRow, 0, len(products))
		var totalRevenue, totalProfit float64
		var totalUnits, inStock, outStock, lowStock int
		ingestion := map[string]int{}

		for _, p := range products {
			s := soldByID[p.ID]
			// Ganancia estimada SOLO si el producto tiene costo definido.
			// Con purchase_price = 0 (costo sin capturar, muy común) la
			// "ganancia" sería = ingresos e INFLARÍA la cifra; en ese caso
			// la marcamos como desconocida y NO la sumamos al total.
			profitKnown := p.PurchasePrice > 0
			profit := 0.0
			if profitKnown {
				profit = s.Revenue - p.PurchasePrice*float64(s.Qty)
			}
			rows = append(rows, productRow{
				ID:              p.ID,
				Name:            p.Name,
				Stock:           p.Stock,
				MinStock:        p.MinStock,
				Price:           p.Price,
				PurchasePrice:   p.PurchasePrice,
				UnitsSold:       s.Qty,
				Revenue:         s.Revenue,
				Profit:          profit,
				ProfitKnown:     profitKnown,
				IngestionMethod: ingestionOrManual(p.IngestionMethod),
			})
			totalRevenue += s.Revenue
			if profitKnown {
				totalProfit += profit
			}
			totalUnits += s.Qty
			switch {
			case p.Stock <= 0:
				outStock++
			case p.MinStock > 0 && p.Stock <= p.MinStock:
				lowStock++
			default:
				inStock++
			}
			ingestion[ingestionOrManual(p.IngestionMethod)]++
		}

		// Más vendidas primero (frecuencia de venta).
		sort.SliceStable(rows, func(i, j int) bool {
			return rows[i].UnitsSold > rows[j].UnitsSold
		})

		catalogURL := ""
		if tenant.StoreSlug != nil && *tenant.StoreSlug != "" {
			catalogURL = publicStoreBaseURL() + "/" + *tenant.StoreSlug
		}

		var lastSale *time.Time
		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
			Select("MAX(created_at)").Scan(&lastSale)

		c.JSON(http.StatusOK, gin.H{
			"logo_url":    tenant.LogoURL,
			"store_slug":  tenant.StoreSlug,
			"catalog_url": catalogURL,
			"summary": gin.H{
				"total_products":      len(products),
				"in_stock":            inStock,
				"out_of_stock":        outStock,
				"low_stock":           lowStock,
				"total_units_sold":    totalUnits,
				"total_revenue":       totalRevenue,
				"estimated_profit":    totalProfit,
				"active_modules":      tenant.CountActiveModules(),
				"ingestion_breakdown": ingestion,
				"last_sale_at":        lastSale,
			},
			"products": rows,
		})
	}
}

func ingestionOrManual(m string) string {
	if m == "" {
		return "manual"
	}
	return m
}
