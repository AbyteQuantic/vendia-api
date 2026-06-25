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

		// Fuentes externas (cadenas del scraping vía VTEX + Google opcional).
		// Si fallan, igual seguimos con los proveedores del tenant.
		markets, extErr := places.NearbyMarkets(
			c.Request.Context(), me.Latitude, me.Longitude, int(radius*1000))

		// Proveedores propios del tenant con ubicación fijada (Spec 081). Estos
		// SIEMPRE se muestran (no dependen de fuentes externas).
		var suppliers []models.Supplier
		db.Where("tenant_id = ? AND NOT (latitude = 0 AND longitude = 0)", tenantID).
			Find(&suppliers)
		for _, sup := range suppliers {
			markets = append(markets, services.NearbyMarket{
				Name:    sup.CompanyName,
				Brand:   "Mi proveedor",
				Address: sup.Address,
				Lat:     sup.Latitude,
				Lng:     sup.Longitude,
				Source:  "proveedor",
			})
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
				"source":      mw.Market.Source, // proveedor | exito | olimpica | google
			})
		}
		resp := gin.H{
			"origin": gin.H{"lat": me.Latitude, "lng": me.Longitude},
			"data":   out,
		}
		// Solo avisamos si las fuentes externas fallaron Y no quedó nada que
		// mostrar (ni proveedores propios) — para no asustar sin necesidad.
		if extErr != nil && len(out) == 0 {
			resp["source_error"] = "No pudimos consultar las cadenas ahora. Intente más tarde."
		}
		c.JSON(http.StatusOK, resp)
	}
}
