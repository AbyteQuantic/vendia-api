// Spec: specs/075-proveedores-b2b/spec.md
package handlers

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// HarvestAlert — una cosecha/lote por vencer con su acción sugerida.
type HarvestAlert struct {
	ProductID        string  `json:"product_id"`
	Name             string  `json:"name"`
	ExpiryDate       string  `json:"expiry_date"`
	DaysLeft         int     `json:"days_left"`
	Stock            float64 `json:"stock"`
	NearbyStoreCount int     `json:"nearby_store_count"`
	SuggestedMessage string  `json:"suggested_message"`
}

// SupplierHarvestAlerts — GET /api/v1/supplier/harvest-alerts?radius_km=5&days=7
// Anti-merma (Spec 075 F4): para el proveedor que llama, lista sus perecederos
// por vencer + cuántas tiendas hay cerca + un mensaje listo para difundir y
// liquidar antes de perder la cosecha. Convierte merma en venta dirigida.
func SupplierHarvestAlerts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		me := middleware.GetTenantID(c)
		var supplier models.Tenant
		if err := db.Where("id = ?", me).First(&supplier).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		radius := defaultNearbyRadiusKm
		if q := c.Query("radius_km"); q != "" {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v > 0 {
				radius = math.Min(v, maxNearbyRadiusKm)
			}
		}
		days := expiringSoonDays
		if q := c.Query("days"); q != "" {
			if v, err := strconv.Atoi(q); err == nil && v > 0 {
				days = v
			}
		}

		// Tiendas cercanas (no proveedores) con ubicación real — solo si el
		// proveedor tiene ubicación; si no, el conteo es 0 pero igual avisamos.
		nearbyStores := 0
		if hasRealLocation(supplier.Latitude, supplier.Longitude) {
			latDelta := radius / 111.0
			cosLat := math.Cos(supplier.Latitude * math.Pi / 180)
			if cosLat < 0.01 {
				cosLat = 0.01
			}
			lonDelta := radius / (111.0 * cosLat)
			var stores []models.Tenant
			db.Where("id <> ?", me).
				Where("business_types NOT LIKE ? AND business_types NOT LIKE ?",
					"%"+models.BusinessTypeProveedorAgricola+"%",
					"%"+models.BusinessTypeProveedorMayorista+"%").
				Where("NOT (latitude = 0 AND longitude = 0)").
				Where("latitude BETWEEN ? AND ?", supplier.Latitude-latDelta, supplier.Latitude+latDelta).
				Where("longitude BETWEEN ? AND ?", supplier.Longitude-lonDelta, supplier.Longitude+lonDelta).
				Find(&stores)
			for _, s := range stores {
				if haversineKm(supplier.Latitude, supplier.Longitude, s.Latitude, s.Longitude) <= radius {
					nearbyStores++
				}
			}
		}

		// Perecederos por vencer dentro de `days`.
		var rows []struct {
			ID         string
			Name       string
			Stock      float64
			ExpiryDate *string
		}
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND deleted_at IS NULL AND expiry_date IS NOT NULL AND expiry_date <> ''", me).
			Select("id, name, stock, expiry_date").Scan(&rows)

		today := time.Now()
		cutoff := today.AddDate(0, 0, days)
		alerts := make([]HarvestAlert, 0)
		for _, r := range rows {
			if r.ExpiryDate == nil {
				continue
			}
			raw := *r.ExpiryDate
			if len(raw) > 10 {
				raw = raw[:10]
			}
			exp, err := time.Parse("2006-01-02", raw)
			if err != nil || exp.After(cutoff) {
				continue
			}
			daysLeft := int(math.Ceil(exp.Sub(today).Hours() / 24))
			alerts = append(alerts, HarvestAlert{
				ProductID:        r.ID,
				Name:             r.Name,
				ExpiryDate:       raw,
				DaysLeft:         daysLeft,
				Stock:            r.Stock,
				NearbyStoreCount: nearbyStores,
				SuggestedMessage: buildHarvestMessage(supplier.BusinessName, r.Name, daysLeft, nearbyStores),
			})
		}

		c.JSON(http.StatusOK, gin.H{"data": alerts})
	}
}

func buildHarvestMessage(supplierName, product string, daysLeft, nearbyStores int) string {
	when := "pronto"
	if daysLeft <= 0 {
		when = "hoy"
	} else if daysLeft == 1 {
		when = "mañana"
	} else {
		when = fmt.Sprintf("en %d días", daysLeft)
	}
	return fmt.Sprintf(
		"¡Oferta de %s! Tengo %s fresco que debo vender %s, a muy buen precio. "+
			"Pídamelo directo y le sale más económico por estar cerca. ¿Le interesa?",
		supplierName, product, when)
}
