// Spec: specs/072-captura-ubicacion-gps-osm/spec.md
package handlers

import (
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UpdateStoreLocation — PATCH /api/v1/store/location
// Guarda la ubicación del negocio (lat/long + precisión + referencias) y, si hay
// geocoder, deriva la CIUDAD por reverse-geocode (Nominatim) para alimentar el
// cron de scraping por ciudad. Tolerante a fallos: si el geocoder falla, igual
// guarda lat/long (no bloquea — Art. II offline-friendly).
func UpdateStoreLocation(db *gorm.DB, geo services.Geocoder) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req struct {
			Latitude   float64 `json:"latitude"`
			Longitude  float64 `json:"longitude"`
			Accuracy   float64 `json:"accuracy"`
			References string  `json:"references"`
			// City la manda el cliente cuando ya la resolvió con el geocoder
			// NATIVO del móvil (placemarkFromCoordinates). Es la ruta primaria y
			// evita depender de un geocoder de servidor. Opcional.
			City string `json:"city"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "datos inválidos"})
			return
		}
		// (0,0) no es una ubicación válida.
		if req.Latitude == 0 && req.Longitude == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "Ubicación inválida. Toque “Usar mi ubicación actual”."})
			return
		}

		updates := map[string]any{
			"latitude":            req.Latitude,
			"longitude":           req.Longitude,
			"location_accuracy":   req.Accuracy,
			"location_references": strings.TrimSpace(req.References),
		}

		// Ciudad: 1) la del cliente (geocoder nativo del móvil) si vino;
		// 2) fallback al geocoder de servidor (Photon, cloud-friendly).
		city := strings.TrimSpace(req.City)
		var label string
		if city == "" && geo != nil {
			if l, ct, err := geo.Reverse(req.Latitude, req.Longitude); err == nil {
				label, city = l, ct
			}
		}
		if city != "" {
			updates["city"] = city
		}
		// Solo rellena la dirección si está vacía (no pisa lo escrito a mano).
		if label != "" {
			var current models.Tenant
			if e := db.Select("address").Where("id = ?", tenantID).First(&current).Error; e == nil {
				if strings.TrimSpace(current.Address) == "" {
					updates["address"] = label
				}
			}
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la ubicación"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"latitude": req.Latitude, "longitude": req.Longitude,
			"city": city, "address": label,
		}})
	}
}
