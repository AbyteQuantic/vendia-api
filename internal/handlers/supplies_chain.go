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
//
// sources es inyectable (siempre services.DefaultChainSources en producción,
// ver cmd/server/main.go) para poder testear el flujo completo del handler
// contra un httptest.Server en vez de pegarle a Éxito/Olímpica reales.
func ScrapeChainsJob(db *gorm.DB, sources []services.ChainSource) gin.HandlerFunc {
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
			scraped += services.ScrapeChainsForCity(db, city, sources)
		}
		purged := services.PurgeOldChainPrices(db)
		body := gin.H{"scraped": scraped, "purged": purged, "cities": cities}

		// Auditoría 2026-07-03: antes SIEMPRE devolvía 200, incluso si las DOS
		// cadenas fallaron (API caída, bloqueo por User-Agent, cambio de
		// contrato) y no se insertó ni una fila — el cron "corría bien" cada
		// semana sin que nadie se enterara de que llevaba meses sin traer
		// datos nuevos. Éxito y Olímpica cubren "arroz"/"aceite" a nivel
		// nacional casi siempre, así que scraped=0 es señal real de fallo,
		// no ruido esperado. HTTP 502 → el step de cron-jobs.yml falla
		// (chequea "= 200") → GitHub avisa al dueño del workflow, mismo
		// patrón que capacity-check (507).
		if scraped == 0 {
			c.JSON(http.StatusBadGateway, body)
			return
		}
		c.JSON(http.StatusOK, body)
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
