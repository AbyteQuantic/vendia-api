// Spec: specs/031-cotizaciones/spec.md
package handlers

import (
	"net/http"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetPublicQuote serves the customer-facing quote document by its
// unguessable public token (Spec F031 AC-07). No JWT — the token is the
// only credential, same pattern as the public fiado handshake.
//
// The read lazily expires a quote whose valid_until has passed (plan
// D7 — a safety net for the hourly cron). The response carries enough
// tenant branding for the public page to render the document, and a
// `can_decide` flag that is true ONLY while the quote is `enviada`.
// GET /api/v1/public/quotes/:token
func GetPublicQuote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Param("token")
		if !models.IsValidUUID(token) {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		var quote models.Quote
		if err := db.Preload("Items", func(d *gorm.DB) *gorm.DB {
			return d.Order("quote_items.sort_order ASC")
		}).
			Preload("Customer").
			Where("public_token = ?", token).
			First(&quote).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		// Lazy expire BEFORE rendering so a stale `enviada` quote is shown
		// as `vencida` (and without the approve/reject buttons).
		lazyExpireQuote(db, &quote)

		var tenant models.Tenant
		if err := db.Select("business_name", "nit", "razon_social", "address", "logo_url", "phone").
			Where("id = ?", quote.TenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		customerName := ""
		if quote.Customer != nil {
			customerName = quote.Customer.Name
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"folio":          quote.Folio,
				"status":         quote.Status,
				"valid_until":    quote.ValidUntil,
				"note":           quote.Note,
				"customer_name":  customerName,
				"discount_total": quote.DiscountTotal,
				"tax_rate":       quote.TaxRate,
				"subtotal":       quote.Subtotal,
				"tax_amount":     quote.TaxAmount,
				"total":          quote.Total,
				"items":          quote.Items,
				// can_decide gates the Aprobar/Rechazar buttons on the
				// public page — true ONLY while still awaiting a decision.
				"can_decide": quote.Status == models.QuoteStatusSent,
				"business": gin.H{
					"name":         tenant.BusinessName,
					"nit":          tenant.NIT,
					"razon_social": tenant.RazonSocial,
					"address":      tenant.Address,
					"logo_url":     tenant.LogoURL,
					"phone":        tenant.Phone,
				},
			},
		})
	}
}

// DecidePublicQuote records the customer's approve/reject decision from
// the public link (Spec F031 AC-08). It validates the quote is still
// `enviada` (lazy-expiring it first if overdue), stamps decided_at +
// decided_by_ip as lightweight evidence, and notifies the owner.
// POST /api/v1/public/quotes/:token/decide
func DecidePublicQuote(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		Decision string `json:"decision"`
	}
	return func(c *gin.Context) {
		token := c.Param("token")
		if !models.IsValidUUID(token) {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		var target string
		switch req.Decision {
		case "approve":
			target = models.QuoteStatusApproved
		case "reject":
			target = models.QuoteStatusRejected
		default:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "decisión inválida: usa 'approve' o 'reject'",
			})
			return
		}

		var quote models.Quote
		if err := db.Where("public_token = ?", token).First(&quote).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		// Lazy expire — a quote read after valid_until can no longer be
		// decided. Doing this BEFORE the FSM check turns a stale `enviada`
		// into `vencida`, which then fails CanTransition cleanly.
		lazyExpireQuote(db, &quote)

		if !services.CanTransition(quote.Status, target) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "esta cotización ya no puede recibir una respuesta",
			})
			return
		}

		if err := db.Model(&models.Quote{}).Where("id = ?", quote.ID).
			Updates(map[string]any{
				"status":        target,
				"decided_at":    time.Now().UTC(),
				"decided_by_ip": c.ClientIP(),
			}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo registrar la respuesta",
			})
			return
		}

		// Notify the owner so the decision surfaces in-app (AC-08).
		// Best-effort — a notification miss must never fail the customer's
		// decision.
		notifyOwnerOfQuoteDecision(db, quote, target)

		c.JSON(http.StatusOK, gin.H{
			"message": "respuesta registrada",
			"status":  target,
		})
	}
}

// notifyOwnerOfQuoteDecision writes a Notification row for the tenant so
// the owner sees the customer's approve/reject in "Cotizaciones". Errors
// are swallowed — the customer's decision is already persisted.
func notifyOwnerOfQuoteDecision(db *gorm.DB, quote models.Quote, decision string) {
	verb := "aprobó"
	if decision == models.QuoteStatusRejected {
		verb = "rechazó"
	}
	title := "Cotización " + quote.Folio
	body := "El cliente " + verb + " la cotización " + quote.Folio + "."

	_ = db.Create(&models.Notification{
		TenantID: quote.TenantID,
		Type:     "quote_decision",
		Title:    title,
		Body:     body,
	}).Error
}
