// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"errors"
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// requireEventAdmin enforces that only the admin role can create/edit events
// (spec §5 Seguridad). Cashiers may sell, but event configuration is owner-only.
// Returns false and writes a 403 when the actor is not an admin.
func requireEventAdmin(c *gin.Context) bool {
	if middleware.GetRole(c) != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "solo el administrador puede gestionar eventos"})
		return false
	}
	return true
}

// eventInput is the request body shared by create and update. Money and
// identity fields are validated by the model; status transitions go through
// the dedicated publish/delete endpoints.
type eventInput struct {
	ID                    string                    `json:"id"`
	Type                  string                    `json:"type"`
	Title                 string                    `json:"title"`
	Description           string                    `json:"description"`
	StartAt               *string                   `json:"start_at"`
	EndAt                 *string                   `json:"end_at"`
	Modality              string                    `json:"modality"`
	LocationOrLink        string                    `json:"location_or_link"`
	Capacity              int                       `json:"capacity"`
	Price                 int64                     `json:"price"`
	EnabledPaymentMethods []string                  `json:"enabled_payment_methods"`
	InstallmentsEnabled   bool                      `json:"installments_enabled"`
	InstallmentsCount     int                       `json:"installments_count"`
	CustomFields          []models.EventCustomField `json:"custom_fields"`
	Sessions              []models.EventSession     `json:"sessions"`
	AttendanceRule        string                    `json:"attendance_rule"`
	AttendancePct         int                       `json:"attendance_pct"`
}

func (in eventInput) toModel() *models.Event {
	e := &models.Event{
		Type:                  in.Type,
		Title:                 in.Title,
		Description:           in.Description,
		Modality:              in.Modality,
		LocationOrLink:        in.LocationOrLink,
		Capacity:              in.Capacity,
		Price:                 in.Price,
		EnabledPaymentMethods: in.EnabledPaymentMethods,
		InstallmentsEnabled:   in.InstallmentsEnabled,
		InstallmentsCount:     in.InstallmentsCount,
		CustomFields:          in.CustomFields,
		Sessions:              in.Sessions,
		AttendanceRule:        in.AttendanceRule,
		AttendancePct:         in.AttendancePct,
	}
	if in.AttendanceRule == "" {
		e.AttendanceRule = models.AttendanceRuleInOut
	}
	if in.Modality == "" {
		e.Modality = models.EventModalityPresencial
	}
	if in.Type == "" {
		e.Type = models.EventTypeOtro
	}
	return e
}

// CreateEvent — POST /api/v1/events (admin).
func CreateEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		var in eventInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if in.ID != "" && !models.IsValidUUID(in.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID válido"})
			return
		}
		e := in.toModel()
		if in.ID != "" {
			e.ID = in.ID
		}
		created, err := services.NewEventService(db).Create(middleware.GetTenantID(c), e)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": created})
	}
}

// ListEvents — GET /api/v1/events?status= (admin/cashier read).
func ListEvents(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		events, err := services.NewEventService(db).List(middleware.GetTenantID(c), c.Query("status"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al listar eventos"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": events})
	}
}

// GetEvent — GET /api/v1/events/:id.
func GetEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		e, err := services.NewEventService(db).Get(middleware.GetTenantID(c), c.Param("id"))
		if err != nil {
			writeEventError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": e})
	}
}

// UpdateEvent — PATCH /api/v1/events/:id (admin).
func UpdateEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		var in eventInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		updated, err := services.NewEventService(db).Update(middleware.GetTenantID(c), c.Param("id"), in.toModel())
		if err != nil {
			writeEventError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": updated})
	}
}

// DeleteEvent — DELETE /api/v1/events/:id (admin). Archives, never hard-deletes.
func DeleteEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		e, err := services.NewEventService(db).Archive(middleware.GetTenantID(c), c.Param("id"))
		if err != nil {
			writeEventError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": e})
	}
}

// PublishEvent — POST /api/v1/events/:id/publish (admin).
func PublishEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		e, err := services.NewEventService(db).Publish(middleware.GetTenantID(c), c.Param("id"))
		if err != nil {
			writeEventError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": e})
	}
}

// CheckinEvent — POST /api/v1/events/:id/checkin (admin). Records an entrada
// or salida scan of an attendee's badge QR. Idempotent: a repeated scan
// returns 200 with already_registered=true (spec FR-15, AC-08, AC-11).
func CheckinEvent(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		QRToken      string `json:"qr_token" binding:"required"`
		ScanType     string `json:"scan_type" binding:"required"`
		SessionIndex int    `json:"session_index"`
	}
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		scan, created, err := services.NewEventCheckinService(db).RecordScan(
			middleware.GetTenantID(c), req.QRToken, req.ScanType, req.SessionIndex, middleware.GetUserID(c))
		if err != nil {
			if errors.Is(err, services.ErrRegistrationNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "inscripción no encontrada para ese código"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": scan, "already_registered": !created})
	}
}

// IssueCertificate — POST /api/v1/events/:id/registrations/:rid/certificate
// (admin). Manually issues the certificate for an eligible attendee (FR-17).
func IssueCertificate(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		reg, err := services.NewEventCertificateService(db).Issue(middleware.GetTenantID(c), c.Param("rid"))
		if err != nil {
			switch {
			case errors.Is(err, services.ErrRegistrationNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "inscripción no encontrada"})
			case errors.Is(err, services.ErrCertificateNotEligible):
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al emitir certificado"})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": reg})
	}
}

// writeEventError maps service errors to HTTP status codes with a Spanish
// message (Art. V) — not-found vs. validation vs. server error.
func writeEventError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrEventNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
