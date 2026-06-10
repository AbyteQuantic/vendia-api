// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// maxProofBytes caps an uploaded payment proof image.
const maxProofBytes = 8 << 20

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
		Email         string         `json:"email"`
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
			Email:         req.Email,
			FormData:      req.FormData,
			ConsentComms:  req.ConsentComms,
			PaymentMethod: req.PaymentMethod,
		})
		if err != nil {
			writePublicEventError(c, err)
			return
		}
		// El carné (qr_token) solo se entrega cuando el pago está completo
		// (eventos gratuitos quedan confirmados al instante). Para los de pago
		// pendiente devolvemos el public_token para consultar el carné luego.
		out := gin.H{
			"public_token":   reg.PublicToken,
			"payment_status": reg.PaymentStatus,
			"confirmed":      reg.IsConfirmed(),
		}
		if reg.IsConfirmed() {
			out["qr_token"] = reg.QRToken
		}
		c.JSON(http.StatusCreated, gin.H{"data": out})
	}
}

// PublicFindRegistration — POST /api/v1/store/:slug/my-event-registration.
// Recovers an attendee's event registration token by phone, so the catalog can
// show their event component (cuenta regresiva, pago) WITHOUT the email deep
// link — for someone who registered on another device or was added by the
// organizer. Phone-based (mismo modelo de confianza que "Mis pedidos"). Prefers
// a still-pending registration; falls back to the most recent.
func PublicFindRegistration(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		Phone string `json:"phone"`
	}
	return func(c *gin.Context) {
		tenant, ok := resolveStoreTenant(c, db)
		if !ok {
			return
		}
		var req request
		_ = c.ShouldBindJSON(&req)
		phone := services.NormalizePhone(req.Phone)
		if phone == "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "sin inscripción"})
			return
		}

		var customer models.Customer
		if err := db.Where("tenant_id = ? AND phone = ?", tenant.ID, phone).First(&customer).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sin inscripción"})
			return
		}

		// Prefer a pending registration (la que debe pagar); si no, la última.
		var reg models.EventRegistration
		q := db.Where("tenant_id = ? AND customer_id = ?", tenant.ID, customer.ID)
		err := q.Session(&gorm.Session{}).
			Order("CASE WHEN payment_status = 'pending' THEN 0 ELSE 1 END, created_at DESC").
			First(&reg).Error
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sin inscripción"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"public_token": reg.PublicToken}})
	}
}

// PublicGetCarnet — GET /api/v1/store/:slug/events/registration/:token.
// The attendee's carné portal: returns the inscription status and balance, and
// the QR token ONLY when the payment is complete (spec FR-09). Lets a guest who
// paid in cuotas come back later and find the carné already active.
func PublicGetCarnet(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant, ok := resolveStoreTenant(c, db)
		if !ok {
			return
		}
		reg, err := services.NewEventRegistrationService(db).GetByPublicToken(tenant.ID, c.Param("token"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "carné no encontrado"})
			return
		}
		ev, err := services.NewEventService(db).Get(tenant.ID, reg.EventID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "evento no encontrado"})
			return
		}

		confirmed := reg.IsConfirmed()
		balance := ev.Price - reg.AmountPaid
		if balance < 0 {
			balance = 0
		}
		// Nombre del asistente (lo posee quien tiene el token).
		var customer models.Customer
		_ = db.Where("id = ? AND tenant_id = ?", reg.CustomerID, tenant.ID).First(&customer).Error

		out := gin.H{
			"event_title":          ev.Title,
			"type":                 ev.Type,
			"modality":             ev.Modality,
			"start_at":             ev.StartAt,
			"end_at":               ev.EndAt,
			"location":             ev.LocationOrLink,
			"city":                 ev.City,
			"location_notes":       ev.LocationNotes,
			"attendee_name":        customer.Name,
			"payment_status":       reg.PaymentStatus,
			"amount_paid":          reg.AmountPaid,
			"price":                ev.Price,
			"balance":              balance,
			"currency":             ev.Currency,
			"installments_enabled": ev.InstallmentsEnabled,
			"installments_count":   ev.InstallmentsCount,
			"confirmed":            confirmed,
		}
		// El QR (carné válido) solo viaja cuando el pago está completo.
		// El diseño de escarapela del evento (si el organizador lo configuró)
		// se entrega para pintar el carné con SU diseño, no un QR pelado. El
		// recuadro inferior del diseño reserva el espacio para el QR real.
		if confirmed {
			out["qr_token"] = reg.QRToken
			out["badge_image"] = ev.BadgeTemplate.ImageURL
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// PublicSubmitPaymentProof — POST /api/v1/store/:slug/carnet/:token/proof.
// The attendee reports a manual payment (transfer/cash) and optionally attaches
// a receipt image. It lands as a PENDING payment for the organizer to review
// and approve, which activates the carné — no payment gateway involved.
func PublicSubmitPaymentProof(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant, ok := resolveStoreTenant(c, db)
		if !ok {
			return
		}

		amount, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("amount")), 10, 64)
		if err != nil || amount <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "indique el monto pagado"})
			return
		}
		note := strings.TrimSpace(c.PostForm("note"))

		// Optional receipt image. Stored to R2 (fallback to data URL).
		proofURL := ""
		if file, header, ferr := c.Request.FormFile("image"); ferr == nil {
			defer file.Close()
			if header.Size > maxProofBytes {
				c.JSON(http.StatusBadRequest, gin.H{"error": "la imagen excede 8MB"})
				return
			}
			mime := header.Header.Get("Content-Type")
			if !strings.HasPrefix(mime, "image/") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el comprobante debe ser una imagen"})
				return
			}
			data, rerr := io.ReadAll(file)
			if rerr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer el comprobante"})
				return
			}
			proofURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
			if storageSvc != nil {
				key := fmt.Sprintf("event-proofs/%s/%s-%s", tenant.ID, c.Param("token"), uuid.NewString()[:8])
				if up, uerr := storageSvc.Upload(c.Request.Context(), "event-assets", key, data, mime); uerr == nil {
					proofURL = up
				}
			}
		}

		if _, err := services.NewEventRegistrationService(db).SubmitProof(
			tenant.ID, c.Param("token"), amount, proofURL, note); err != nil {
			writePublicEventError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": gin.H{"status": "pending"}})
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
