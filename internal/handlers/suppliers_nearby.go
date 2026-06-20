// Spec: specs/075-proveedores-b2b/spec.md
package handlers

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NearbySupplier es la vista PÚBLICA y acotada de un proveedor para la tienda
// que descubre (cross-tenant, solo lectura — Art. III). Nunca expone datos
// privados del otro tenant.
type NearbySupplier struct {
	ID                string   `json:"id"`
	BusinessName      string   `json:"business_name"`
	BusinessTypes     []string `json:"business_types"`
	DistanceKm        float64  `json:"distance_km"`
	ProductCount      int      `json:"product_count"`
	ExpiringSoonCount int      `json:"expiring_soon_count"`
}

const (
	defaultNearbyRadiusKm = 5.0
	maxNearbyRadiusKm     = 50.0
	expiringSoonDays      = 7
)

// haversineKm — distancia en km entre dos puntos (gratis, sin Google/PostGIS).
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func hasRealLocation(lat, lon float64) bool { return lat != 0 || lon != 0 }

// SuppliersNearby — GET /api/v1/suppliers/nearby?radius_km=5
// Devuelve los proveedores (business_type proveedor_*) con ubicación real a ≤
// radio de la tienda que llama, ordenados por distancia. Pre-filtro por
// bounding box en SQL (portable), distancia exacta + filtro en Go.
func SuppliersNearby(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var me models.Tenant
		if err := db.Where("id = ?", tenantID).First(&me).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}
		if !hasRealLocation(me.Latitude, me.Longitude) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "Fije la ubicación de su negocio para ver proveedores cercanos."})
			return
		}

		radius := defaultNearbyRadiusKm
		if q := c.Query("radius_km"); q != "" {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v > 0 {
				radius = math.Min(v, maxNearbyRadiusKm)
			}
		}

		// Bounding box: 1° lat ≈ 111 km; 1° lon ≈ 111·cos(lat) km.
		latDelta := radius / 111.0
		cosLat := math.Cos(me.Latitude * math.Pi / 180)
		if cosLat < 0.01 {
			cosLat = 0.01
		}
		lonDelta := radius / (111.0 * cosLat)

		var candidates []models.Tenant
		db.Where("id <> ?", tenantID).
			Where("business_types LIKE ? OR business_types LIKE ?",
				"%"+models.BusinessTypeProveedorAgricola+"%",
				"%"+models.BusinessTypeProveedorMayorista+"%").
			Where("NOT (latitude = 0 AND longitude = 0)").
			Where("latitude BETWEEN ? AND ?", me.Latitude-latDelta, me.Latitude+latDelta).
			Where("longitude BETWEEN ? AND ?", me.Longitude-lonDelta, me.Longitude+lonDelta).
			Find(&candidates)

		type inRange struct {
			t    models.Tenant
			dist float64
		}
		var matched []inRange
		ids := make([]string, 0, len(candidates))
		for _, t := range candidates {
			d := haversineKm(me.Latitude, me.Longitude, t.Latitude, t.Longitude)
			if d <= radius {
				matched = append(matched, inRange{t: t, dist: d})
				ids = append(ids, t.ID)
			}
		}

		// Conteos de producto y de perecederos por vencer (una sola query).
		productCount := map[string]int{}
		expiringCount := map[string]int{}
		if len(ids) > 0 {
			var rows []struct {
				TenantID   string
				ExpiryDate *string
			}
			db.Model(&models.Product{}).
				Where("tenant_id IN ? AND deleted_at IS NULL", ids).
				Select("tenant_id, expiry_date").Scan(&rows)
			soonCutoff := time.Now().AddDate(0, 0, expiringSoonDays)
			for _, r := range rows {
				productCount[r.TenantID]++
				if r.ExpiryDate != nil && *r.ExpiryDate != "" {
					if d, err := time.Parse("2006-01-02", (*r.ExpiryDate)[:min(10, len(*r.ExpiryDate))]); err == nil {
						if !d.After(soonCutoff) {
							expiringCount[r.TenantID]++
						}
					}
				}
			}
		}

		out := make([]NearbySupplier, 0, len(matched))
		for _, m := range matched {
			out = append(out, NearbySupplier{
				ID:                m.t.ID,
				BusinessName:      m.t.BusinessName,
				BusinessTypes:     m.t.BusinessTypes,
				DistanceKm:        math.Round(m.dist*100) / 100,
				ProductCount:      productCount[m.t.ID],
				ExpiringSoonCount: expiringCount[m.t.ID],
			})
		}
		// Orden por distancia ascendente.
		for i := 1; i < len(out); i++ {
			for j := i; j > 0 && out[j].DistanceKm < out[j-1].DistanceKm; j-- {
				out[j], out[j-1] = out[j-1], out[j]
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}
