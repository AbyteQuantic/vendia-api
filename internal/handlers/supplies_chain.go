// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers

import (
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ScrapeChainsJob — POST /api/v1/internal/jobs/scrape-chains (CRON_TOKEN)
// Fase 4 (Spec 077): scrapea Éxito/Olímpica (VTEX) para las CIUDADES donde hay
// tenants registrados, inserta en chain_price (append-only → histórico) y PURGA
// lo de >4 meses en la misma corrida. Semanal. Nunca en el camino del tenant.
func ScrapeChainsJob(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}
		var cities []string
		db.Model(&models.Tenant{}).
			Where("city <> '' AND NOT (latitude = 0 AND longitude = 0)").
			Distinct().Pluck("city", &cities)
		if len(cities) == 0 {
			cities = []string{""} // sin tenants ubicados → referencia nacional
		}
		scraped := 0
		for _, city := range cities {
			scraped += services.ScrapeChainsForCity(db, city, services.DefaultChainSources)
		}
		purged := services.PurgeOldChainPrices(db)
		c.JSON(http.StatusOK, gin.H{
			"scraped": scraped, "purged": purged, "cities": cities,
		})
	}
}

// GetChainPrices — GET /api/v1/supplies/chain-prices?name=arroz
// Precios de referencia en cadenas para un insumo + señal "bajó de precio".
// Lee del histórico cacheado (rápido), filtrado por la ciudad del tenant.
func GetChainPrices(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		name := c.Query("name")
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "falta name"})
			return
		}
		var tenant models.Tenant
		db.Select("city").Where("id = ?", tenantID).First(&tenant)

		matches := services.MatchChainPrices(db, services.NormalizeText(name), tenant.City)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"matches":    matches,
			"disclaimer": "Precios de referencia de catálogos en línea; pueden variar y no están garantizados.",
		}})
	}
}
