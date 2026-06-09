// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// requireEventAdmin gates event management to the tenant's STAFF (admin o
// cashier). En la práctica muchos tenderos dueños quedan con rol `cashier`
// (su negocio lo manejan ellos mismos), igual que el perfil del negocio o las
// capacidades no exigen `admin`. super_admin es plataforma, no gestiona
// eventos de un tenant. Escribe 403 si el actor no es staff del negocio.
func requireEventAdmin(c *gin.Context) bool {
	role := middleware.GetRole(c)
	if role != "admin" && role != "cashier" {
		c.JSON(http.StatusForbidden, gin.H{"error": "no tiene permiso para gestionar eventos"})
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

// ListEventRegistrations — GET /api/v1/events/:id/registrations (admin).
// Returns the attendee panel for an event (FR-16, AC-15).
func ListEventRegistrations(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		views, err := services.NewEventRegistrationService(db).ListByEvent(
			middleware.GetTenantID(c), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al listar inscritos"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": views})
	}
}

// ExportEventRegistrations — GET /api/v1/events/:id/registrations/export (admin).
// Returns the attendee list as CSV (FR-17, AC-15).
func ExportEventRegistrations(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		views, err := services.NewEventRegistrationService(db).ListByEvent(
			middleware.GetTenantID(c), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al exportar inscritos"})
			return
		}
		var b strings.Builder
		b.WriteString("nombre,celular,metodo_pago,estado_pago,asistio_entrada,asistio_salida,certificado\n")
		for _, v := range views {
			b.WriteString(fmt.Sprintf("%s,%s,%s,%s,%t,%t,%t\n",
				csvEsc(v.CustomerName), csvEsc(v.CustomerPhone), csvEsc(v.PaymentMethod),
				v.PaymentStatus, v.CheckedIn, v.CheckedOut, v.CertIssued))
		}
		c.Header("Content-Disposition", "attachment; filename=inscritos.csv")
		c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(b.String()))
	}
}

// csvEsc quotes a field that contains a comma, quote or newline (RFC 4180).
func csvEsc(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
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
