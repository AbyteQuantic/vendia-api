// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"errors"
	"net/http"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// resolveStoreTenant looks up the tenant that owns a public store slug. It
// mirrors PublicCatalog so the events storefront resolves identically. Returns
// false (and writes 404) when the slug is unknown.
func resolveStoreTenant(c *gin.Context, db *gorm.DB) (*models.Tenant, bool) {
	var tenant models.Tenant
	if err := db.Where("store_slug = ?", c.Param("slug")).First(&tenant).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
		return nil, false
	}
	return &tenant, true
}

// PublicListEvents — GET /api/v1/store/:slug/events. Only published events are
// exposed (Art. III: scoped to the slug's tenant).
func PublicListEvents(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant, ok := resolveStoreTenant(c, db)
		if !ok {
			return
		}
		events, err := services.NewEventService(db).List(tenant.ID, models.EventStatusPublicado)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al listar eventos"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": events})
	}
}

// PublicGetEvent — GET /api/v1/store/:slug/events/:id. Only a published event
// of the slug's tenant is returned.
func PublicGetEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant, ok := resolveStoreTenant(c, db)
		if !ok {
			return
		}
		ev, err := services.NewEventService(db).Get(tenant.ID, c.Param("id"))
		if err != nil || ev.Status != models.EventStatusPublicado {
			c.JSON(http.StatusNotFound, gin.H{"error": "evento no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": ev})
	}
}

// PublicRegisterEvent — POST /api/v1/store/:slug/events/:id/register.
// Public inscription: validates consent, deduplicates the attendee into the
// organizer's customers, and returns the registration with its public token.
// Protected by the dedicated rate-limiter + Turnstile in main.go (F025).
func PublicRegisterEvent(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		ID            string         `json:"id"`
		Name          string         `json:"name" binding:"required,min=2"`
		Phone         string         `json:"phone"`
		FormData      map[string]any `json:"form_data"`
		ConsentComms  bool           `json:"consent_comms"`
		PaymentMethod string         `json:"payment_method"`
	}
	return func(c *gin.Context) {
		tenant, ok := resolveStoreTenant(c, db)
		if !ok {
			return
		}
		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		reg, err := services.NewEventRegistrationService(db).Register(tenant.ID, services.RegisterInput{
			EventID:       c.Param("id"),
			ClientID:      req.ID,
			Name:          req.Name,
			Phone:         req.Phone,
			FormData:      req.FormData,
			ConsentComms:  req.ConsentComms,
			PaymentMethod: req.PaymentMethod,
		})
		if err != nil {
			writePublicEventError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": reg})
	}
}

// writePublicEventError maps inscription errors to status codes with Spanish
// messages (Art. V), never leaking internals (Art. VI).
func writePublicEventError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrEventNotFound), errors.Is(err, services.ErrEventNotPublished):
		c.JSON(http.StatusNotFound, gin.H{"error": "evento no disponible"})
	case errors.Is(err, services.ErrConsentRequired), errors.Is(err, services.ErrEventCapacityFull):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
