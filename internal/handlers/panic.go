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
				"panic_message": tenant.PanicMessage,
				"contacts":      contacts,
			},
		})
	}
}

// UpdatePanicMessage updates the panic message text.
func UpdatePanicMessage(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PanicMessage string `json:"panic_message" binding:"required"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		db.Model(&models.Tenant{}).Where("id = ?", tenantID).Update("panic_message", req.PanicMessage)
		c.JSON(http.StatusOK, gin.H{"message": "mensaje de pánico actualizado"})
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
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

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

		message := tenant.PanicMessage
		if message == "" {
			message = fmt.Sprintf("EMERGENCIA en %s. Necesito ayuda inmediata.", tenant.BusinessName)
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
					// TODO: Integrate Twilio SMS API
					log.Printf("[PANIC-SMS] → %s (%s): %s", contact.Name, contact.PhoneNumber, message)
				case "whatsapp":
					// TODO: Integrate Meta WhatsApp Business API
					log.Printf("[PANIC-WA] → %s (%s): %s", contact.Name, contact.PhoneNumber, message)
				default:
					log.Printf("[PANIC] → %s (%s) [%s]: %s", contact.Name, contact.PhoneNumber, contact.ContactMethod, message)
				}
			}
			log.Printf("[PANIC] Alert dispatched to %d contacts for tenant %s", len(contacts), tenantID)
		}()
	}
}
