// Spec: specs/081-mercado-cercano-mapa/spec.md
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

// MarketNearby — GET /api/v1/market/nearby?radius_km=5
//
// Mercados/supermercados reales (Ara, D1, Éxito, Olímpica…) cerca del negocio,
// para el MAPA. Fuente: OpenStreetMap (Overpass) bajo demanda — NO la red B2B de
// tenants. Devuelve el origen (la tienda) + las sedes con coords y distancia.
func MarketNearby(db *gorm.DB, places *services.PlacesService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var me models.Tenant
		if err := db.Where("id = ?", tenantID).First(&me).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}
		if !hasRealLocation(me.Latitude, me.Longitude) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "Fije la ubicación de su negocio para ver el mercado cercano."})
			return
		}

		radius := defaultNearbyRadiusKm
		if q := c.Query("radius_km"); q != "" {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v > 0 {
				radius = math.Min(v, maxNearbyRadiusKm)
			}
		}

		markets, err := places.NearbyMarkets(
			c.Request.Context(), me.Latitude, me.Longitude, int(radius*1000))
		if err != nil {
			// No reventar el mapa: devolvemos el origen para que igual se
			// dibuje, con aviso de que la fuente externa falló.
			c.JSON(http.StatusOK, gin.H{
				"origin": gin.H{"lat": me.Latitude, "lng": me.Longitude},
				"data":   []gin.H{},
				"source_error": "No pudimos consultar el mapa de tiendas ahora. Intente más tarde.",
			})
			return
		}

		sorted := services.SortMarketsByDistance(markets, me.Latitude, me.Longitude, radius)
		out := make([]gin.H, 0, len(sorted))
		for _, mw := range sorted {
			out = append(out, gin.H{
				"name":        mw.Market.Name,
				"brand":       mw.Market.Brand,
				"address":     mw.Market.Address,
				"lat":         mw.Market.Lat,
				"lng":         mw.Market.Lng,
				"distance_km": math.Round(mw.DistKm*10) / 10,
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"origin": gin.H{"lat": me.Latitude, "lng": me.Longitude},
			"data":   out,
		})
	}
}
