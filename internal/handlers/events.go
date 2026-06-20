// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// requireEventAdmin gates event management to the tenant's STAFF. El JWT lleva
// el WorkspaceRole (login.go workspaceRoleFromEmployee): el DUEÑO es "owner",
// un empleado admin "admin", el resto "cashier". El perfil del negocio y las
// capacidades tampoco exigen un rol específico — el tendero maneja su negocio.
// Escribe 403 solo si el actor no es staff del tenant (p. ej. sin rol).
func requireEventAdmin(c *gin.Context) bool {
	switch middleware.GetRole(c) {
	case "owner", "admin", "cashier":
		return true
	default:
		c.JSON(http.StatusForbidden, gin.H{"error": "no tiene permiso para gestionar eventos"})
		return false
	}
}

// eventInput is the request body shared by create and update. Money and
// identity fields are validated by the model; status transitions go through
// the dedicated publish/delete endpoints.
type eventInput struct {
	ID                    string                        `json:"id"`
	Type                  string                        `json:"type"`
	Title                 string                        `json:"title"`
	Description           string                        `json:"description"`
	StartAt               *string                       `json:"start_at"`
	EndAt                 *string                       `json:"end_at"`
	Modality              string                        `json:"modality"`
	LocationOrLink        string                        `json:"location_or_link"`
	City                  string                        `json:"city"`
	LocationNotes         string                        `json:"location_notes"`
	Capacity              int                           `json:"capacity"`
	Price                 int64                         `json:"price"`
	Cost                  int64                         `json:"cost"`
	Currency              string                        `json:"currency"`
	EnabledPaymentMethods []string                      `json:"enabled_payment_methods"`
	PaymentDetails        []models.EventPaymentDetail   `json:"payment_details"`
	InstallmentsEnabled   bool                          `json:"installments_enabled"`
	InstallmentsCount     int                           `json:"installments_count"`
	CustomFields          []models.EventCustomField     `json:"custom_fields"`
	Sessions              []models.EventSession         `json:"sessions"`
	AttendanceRule        string                        `json:"attendance_rule"`
	AttendancePct         int                           `json:"attendance_pct"`
	CertificateConfig     models.EventCertificateConfig `json:"certificate_config"`
}

func (in eventInput) toModel() *models.Event {
	e := &models.Event{
		Type:                  in.Type,
		Title:                 in.Title,
		Description:           in.Description,
		Modality:              in.Modality,
		LocationOrLink:        in.LocationOrLink,
		City:                  in.City,
		LocationNotes:         in.LocationNotes,
		Capacity:              in.Capacity,
		Price:                 in.Price,
		Cost:                  in.Cost,
		Currency:              in.Currency,
		EnabledPaymentMethods: in.EnabledPaymentMethods,
		PaymentDetails:        in.PaymentDetails,
		InstallmentsEnabled:   in.InstallmentsEnabled,
		InstallmentsCount:     in.InstallmentsCount,
		CustomFields:          in.CustomFields,
		Sessions:              in.Sessions,
		AttendanceRule:        in.AttendanceRule,
		AttendancePct:         in.AttendancePct,
		CertificateConfig:     in.CertificateConfig,
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
	// Parse the ISO dates the client sends (previously dropped — the event
	// date never persisted, so the catalog/afiche showed no date).
	e.StartAt = parseEventTime(in.StartAt)
	e.EndAt = parseEventTime(in.EndAt)
	return e
}

// parseEventTime turns an optional ISO-8601 string into a *time.Time. A nil,
// blank or unparseable value yields nil (the field is optional).
func parseEventTime(s *string) *time.Time {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(*s))
	if err != nil {
		return nil
	}
	return &t
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

// CancelEvent — POST /api/v1/events/:id/cancel (admin). Marca el evento como
// cancelado (Spec 069): sale del catálogo público sin borrar inscritos ni
// carné. Espejo de PublishEvent. "Finalizar" usa DELETE /events/:id (archivado).
func CancelEvent(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		e, err := services.NewEventService(db).Cancel(middleware.GetTenantID(c), c.Param("id"))
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

// UpdateEventCertificateConfig — PUT /api/v1/events/:id/certificate-config
// (admin). Guarda SOLO la configuración del certificado (texto, firma, logo y
// layout de posiciones), sin tocar el resto del evento. Lo usa el diseñador
// de certificado (WYSIWYG).
func UpdateEventCertificateConfig(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		tenantID := middleware.GetTenantID(c)
		var cfg models.EventCertificateConfig
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ev, err := services.NewEventService(db).Get(tenantID, c.Param("id"))
		if err != nil {
			writeEventError(c, err)
			return
		}
		ev.CertificateConfig = cfg
		if err := db.Save(ev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar el certificado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": ev})
	}
}

// UpdateEventBadgeConfig — PUT /api/v1/events/:id/badge-config (admin). Guarda
// SOLO la configuración del CARNÉ/escarapela (texto, firma, logo y layout de
// posiciones), sin tocar el resto del evento. Lo usa el diseñador WYSIWYG del
// carné — espejo de UpdateEventCertificateConfig (misma forma de config).
func UpdateEventBadgeConfig(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		tenantID := middleware.GetTenantID(c)
		var cfg models.EventCertificateConfig
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ev, err := services.NewEventService(db).Get(tenantID, c.Param("id"))
		if err != nil {
			writeEventError(c, err)
			return
		}
		ev.BadgeConfig = cfg
		if err := db.Save(ev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar el carné"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": ev})
	}
}

// IssueAllCertificates — POST /api/v1/events/:id/certificates/issue-all
// (admin). Envío masivo: emite el certificado a todos los asistentes que
// registraron entrada y salida (elegibles) y aún no lo tenían.
func IssueAllCertificates(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		n, err := services.NewEventCertificateService(db).IssueAllEligible(
			middleware.GetTenantID(c), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al emitir los certificados"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"issued": n}})
	}
}

// RecordRegistrationPayment — POST /api/v1/events/:id/registrations/:rid/payments
// (admin). Registers an abono (cuota or full payment) the organizer received
// off-platform; once the running total reaches the price the carné activates.
func RecordRegistrationPayment(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		Amount int64 `json:"amount" binding:"required"`
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
		reg, err := services.NewEventRegistrationService(db).RecordPayment(
			middleware.GetTenantID(c), c.Param("rid"), req.Amount)
		if err != nil {
			writeRegistrationPaymentError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": reg})
	}
}

// ConfirmRegistrationPayment — POST /api/v1/events/:id/registrations/:rid/confirm-payment
// (admin). Marks the inscription as fully paid in one step (carné activado).
func ConfirmRegistrationPayment(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		reg, err := services.NewEventRegistrationService(db).ConfirmPayment(
			middleware.GetTenantID(c), c.Param("rid"))
		if err != nil {
			writeRegistrationPaymentError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": reg})
	}
}

func writeRegistrationPaymentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrRegistrationNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "inscripción no encontrada"})
	case errors.Is(err, services.ErrEventCapacityFull),
		errors.Is(err, services.ErrPaymentAmountInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error al registrar el pago"})
	}
}

// AssignRegistrationSeat — PUT /api/v1/events/:id/registrations/:rid/seat
// (admin). Asigna, cambia o libera la silla de un asistente desde el mapa de
// sillas. Body: {"seat_number": <int>} para asignar/mover, o
// {"seat_number": null} (o ausente) para liberar.
func AssignRegistrationSeat(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		SeatNumber *int `json:"seat_number"`
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
		reg, err := services.NewEventRegistrationService(db).AssignSeat(
			middleware.GetTenantID(c), c.Param("rid"), req.SeatNumber)
		if err != nil {
			switch {
			case errors.Is(err, services.ErrRegistrationNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "inscripción no encontrada"})
			case errors.Is(err, services.ErrSeatInvalid),
				errors.Is(err, services.ErrSeatTaken):
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al asignar la silla"})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": reg})
	}
}

// ListEventPayments — GET /api/v1/events/:id/payments (admin). Returns the
// event's payment proofs (filter with ?status=pending) for review.
func ListEventPayments(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		views, err := services.NewEventRegistrationService(db).ListPayments(
			middleware.GetTenantID(c), c.Param("id"), c.Query("status"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al listar pagos"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": views})
	}
}

// ApproveEventPayment — POST /api/v1/events/:id/payments/:pid/approve (admin).
// Approves a payment proof; its amount is counted and the carné activates when
// the price is reached.
func ApproveEventPayment(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		reg, err := services.NewEventRegistrationService(db).ApprovePayment(
			middleware.GetTenantID(c), c.Param("pid"))
		if err != nil {
			writeRegistrationPaymentError(c, err)
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
