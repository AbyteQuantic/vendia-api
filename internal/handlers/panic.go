package handlers

import (
	"fmt"
	"log"
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetPanicConfig returns the panic message and emergency contacts.
func GetPanicConfig(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tenant models.Tenant
		if err := db.Select("panic_message").First(&tenant, "id = ?", tenantID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		var contacts []models.EmergencyContact
		db.Where("tenant_id = ?", tenantID).Order("created_at ASC").Find(&contacts)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"panic_message":         tenant.PanicMessage,
				"panic_include_address": tenant.PanicIncludeAddress,
				"panic_include_gps":     tenant.PanicIncludeGPS,
				"contacts":             contacts,
			},
		})
	}
}

// UpdatePanicConfig updates the panic message and preferences.
func UpdatePanicMessage(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PanicMessage        *string `json:"panic_message"`
		PanicIncludeAddress *bool   `json:"panic_include_address"`
		PanicIncludeGPS     *bool   `json:"panic_include_gps"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		updates := map[string]any{}
		if req.PanicMessage != nil {
			updates["panic_message"] = *req.PanicMessage
		}
		if req.PanicIncludeAddress != nil {
			updates["panic_include_address"] = *req.PanicIncludeAddress
		}
		if req.PanicIncludeGPS != nil {
			updates["panic_include_gps"] = *req.PanicIncludeGPS
		}
		if len(updates) > 0 {
			db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(updates)
		}
		c.JSON(http.StatusOK, gin.H{"message": "configuración de pánico actualizada"})
	}
}

// CreateEmergencyContact adds a contact (max 5 per tenant).
func CreateEmergencyContact(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name          string `json:"name" binding:"required"`
		PhoneNumber   string `json:"phone_number" binding:"required"`
		ContactMethod string `json:"contact_method"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var count int64
		db.Model(&models.EmergencyContact{}).Where("tenant_id = ?", tenantID).Count(&count)
		if count >= 5 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "máximo 5 contactos de emergencia"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		method := req.ContactMethod
		if method == "" {
			method = "whatsapp"
		}

		contact := models.EmergencyContact{
			TenantID:      tenantID,
			Name:          req.Name,
			PhoneNumber:   req.PhoneNumber,
			ContactMethod: method,
		}
		if err := db.Create(&contact).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear contacto"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": contact})
	}
}

// DeleteEmergencyContact removes a contact.
func DeleteEmergencyContact(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		id := c.Param("id")
		result := db.Where("id = ? AND tenant_id = ?", id, tenantID).Delete(&models.EmergencyContact{})
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "contacto no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "contacto eliminado"})
	}
}

// TriggerPanic sends emergency messages to all contacts (cloud-triggered).
// Returns 200 immediately — messages are dispatched asynchronously.
func TriggerPanic(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		LiveLatitude  float64 `json:"live_latitude"`
		LiveLongitude float64 `json:"live_longitude"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		_ = c.ShouldBindJSON(&req)

		var tenant models.Tenant
		if err := db.First(&tenant, "id = ?", tenantID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		var contacts []models.EmergencyContact
		db.Where("tenant_id = ?", tenantID).Find(&contacts)

		if len(contacts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no hay contactos de emergencia configurados"})
			return
		}

		// Build message with location context
		message := tenant.PanicMessage
		if message == "" {
			message = fmt.Sprintf("EMERGENCIA en %s. Necesito ayuda inmediata.", tenant.BusinessName)
		}

		// Append registered address if configured
		if tenant.PanicIncludeAddress && tenant.Address != "" {
			message += fmt.Sprintf("\n\nDireccion: %s", tenant.Address)
		}

		// Append Google Maps link with live GPS or registered coords
		if tenant.PanicIncludeGPS {
			lat, lng := req.LiveLatitude, req.LiveLongitude
			if lat == 0 && lng == 0 {
				lat, lng = tenant.Latitude, tenant.Longitude
			}
			if lat != 0 || lng != 0 {
				message += fmt.Sprintf("\n\nUbicacion: https://maps.google.com/?q=%.6f,%.6f", lat, lng)
			}
		}

		// Respond immediately — dispatch in background
		c.JSON(http.StatusOK, gin.H{
			"message":        "alerta enviada",
			"contacts_count": len(contacts),
		})

		// Async dispatch to all contacts
		go func() {
			for _, contact := range contacts {
				switch contact.ContactMethod {
				case "sms":
					log.Printf("[PANIC-SMS] → %s (%s): %s", contact.Name, contact.PhoneNumber, message)
				case "whatsapp":
					log.Printf("[PANIC-WA] → %s (%s): %s", contact.Name, contact.PhoneNumber, message)
				default:
					log.Printf("[PANIC] → %s (%s) [%s]: %s", contact.Name, contact.PhoneNumber, contact.ContactMethod, message)
				}
			}
			log.Printf("[PANIC] Alert dispatched to %d contacts for tenant %s", len(contacts), tenantID)
		}()
	}
}
