// Spec: specs/008-planes-suscripcion-epayco/spec.md
//
// Subscription handlers — Feature 008. Five endpoints:
//
//	GET  /api/v1/subscription/plans               (JWT)    catalogo
//	GET  /api/v1/subscription/status              (JWT)    estado del tenant
//	POST /api/v1/subscription/checkout            (JWT)    arma checkout ePayco
//	POST /api/v1/subscription/epayco/confirmation (PUBLIC) webhook de ePayco
//	GET  /api/v1/subscription/response            (PUBLIC) landing post-pago
//
// The confirmation webhook is PUBLIC, so it verifies the ePayco
// signature before trusting anything (Art. VI / AC-06) and is
// idempotent on epayco_transaction_id (Art. II / AC-07).
package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vendia-backend/internal/billing"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ── GET /api/v1/subscription/plans ──────────────────────────────────

// GetSubscriptionPlans returns the read-only plan catalogue (FR-01 /
// AC-03). No DB access — the catalogue is backend config (D4).
func GetSubscriptionPlans() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": billing.Catalog()})
	}
}

// ── GET /api/v1/subscription/status ─────────────────────────────────

// subscriptionStatusResponse is the shape the Flutter app and admin
// read to render the subscription badge / paywall.
//
// TrialTotalDays (Feature 009) is the fixed length of the courtesy
// trial (models.TrialDays, 7). It is a product constant — the same for
// every tenant — so the dashboard can draw the trial progress bar
// (days remaining over total) without hardcoding the denominator.
type subscriptionStatusResponse struct {
	Status             string     `json:"status"` // efectivo, ya degradado
	Plan               string     `json:"plan"`
	Interval           string     `json:"interval,omitempty"`
	IsPremium          bool       `json:"is_premium"`
	TrialEndsAt        *time.Time `json:"trial_ends_at,omitempty"`
	TrialDaysRemaining int        `json:"trial_days_remaining"`
	TrialTotalDays     int        `json:"trial_total_days"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end,omitempty"`
}

// GetSubscriptionStatus reports the tenant's current subscription
// state. It applies time-based degradation (FR-06 / AC-08): an expired
// TRIAL or PRO_ACTIVE is reported — and persisted — as FREE. A tenant
// with no row reads as FREE (never premium).
func GetSubscriptionStatus(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token requerido"})
			return
		}

		now := time.Now()
		var sub models.TenantSubscription
		err := db.Where("tenant_id = ?", tenantID).First(&sub).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Sin fila: FREE. El backfill del bootstrap deberia haberla
			// creado; si falta, no inventamos premium.
			c.JSON(http.StatusOK, subscriptionStatusResponse{
				Status:         models.SubscriptionStatusFree,
				Plan:           models.SubscriptionPlanFree,
				IsPremium:      false,
				TrialTotalDays: models.TrialDays,
			})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al consultar la suscripción",
			})
			return
		}

		effective := sub.EffectiveStatus(now)

		// Write-through: si el estado vigente degrado a FREE, persiste el
		// cambio para que el dashboard y el middleware no reevaluen el
		// reloj en cada request. Un error aqui es solo log — nunca debe
		// impedir responder el estado.
		if effective == models.SubscriptionStatusFree &&
			sub.Status != models.SubscriptionStatusFree {
			db.Model(&models.TenantSubscription{}).
				Where("tenant_id = ?", tenantID).
				Updates(map[string]any{
					"status":     models.SubscriptionStatusFree,
					"updated_at": now,
				})
		}

		c.JSON(http.StatusOK, subscriptionStatusResponse{
			Status:             effective,
			Plan:               sub.Plan,
			Interval:           sub.Interval,
			IsPremium:          sub.IsPremium(now),
			TrialEndsAt:        sub.TrialEndsAt,
			TrialDaysRemaining: sub.TrialDaysRemaining(now),
			TrialTotalDays:     models.TrialDays,
			CurrentPeriodEnd:   sub.CurrentPeriodEnd,
		})
	}
}

// ── POST /api/v1/subscription/checkout ──────────────────────────────

// CreateSubscriptionCheckout starts an ePayco checkout for a {plan,
// interval} purchase (FR-04 / AC-04).
//
// F008 reconciliation — why this returns a URL, not raw widget params:
//
//	The ePayco checkout is a browser-only JS widget. A Flutter app
//	(web + mobile) cannot host it, so this handler does NOT hand the
//	client the raw widget params. Instead it:
//	  1. derives the price and a unique reference,
//	  2. persists a SubscriptionCheckout row (the bridge row),
//	  3. returns {checkout_url, reference, amount, plan, interval},
//	     where checkout_url is <base>/api/v1/subscription/pay/<ref> —
//	     the backend-served page that opens the ePayco widget.
//
//	The Flutter CTA opens checkout_url with launchUrl. The FREE plan is
//	not billable, so a checkout for it is a 400.
func CreateSubscriptionCheckout(db *gorm.DB, epayco *services.EpaycoService) gin.HandlerFunc {
	type request struct {
		Plan     string `json:"plan"     binding:"required"`
		Interval string `json:"interval" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token requerido"})
			return
		}

		if !epayco.IsConfigured() {
			// Sin credenciales no se puede cobrar — fallar fuerte y claro
			// en vez de armar un checkout con llave vacia.
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "la pasarela de pagos no está disponible por ahora",
			})
			return
		}

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// El plan Gratis no se cobra: rechazar antes de mirar el precio.
		if req.Plan == billing.PlanFree {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "el plan Gratis no requiere pago",
			})
			return
		}

		price, err := billing.LookupPrice(req.Plan, req.Interval)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "plan o periodo no válido",
			})
			return
		}

		ref := epayco.GenerateReference(tenantID)
		base := publicBaseURL(c)
		responseURL := base + "/api/v1/subscription/response"
		confirmationURL := base + "/api/v1/subscription/epayco/confirmation"

		// Persistir el checkout: es el puente entre "checkout pedido" y
		// "página de pago servida". GET /subscription/pay/:ref lee esta
		// fila para renderizar el widget. La descripción se deriva igual
		// que en BuildCheckout para que la página y el widget coincidan.
		row := models.SubscriptionCheckout{
			TenantID:        tenantID,
			Reference:       ref,
			Plan:            req.Plan,
			Interval:        req.Interval,
			Amount:          price.Amount,
			Description:     checkoutDescription(req.Interval),
			ResponseURL:     responseURL,
			ConfirmationURL: confirmationURL,
		}
		if err := db.Create(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo iniciar el checkout",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"checkout_url": base + "/api/v1/subscription/pay/" + ref,
			"reference":    ref,
			"amount":       price.Amount,
			"plan":         req.Plan,
			"interval":     req.Interval,
		}})
	}
}

// checkoutDescription is the Spanish buyer-facing description for a
// VendIA Pro purchase. Kept in sync with EpaycoService.BuildCheckout so
// the persisted row and the served widget agree.
func checkoutDescription(interval string) string {
	label := "mensual"
	if interval == billing.IntervalYearly {
		label = "anual"
	}
	return "Suscripción VendIA Pro (" + label + ")"
}

// ── GET /api/v1/subscription/pay/:ref ───────────────────────────────

// SubscriptionPayPage SERVES the ePayco checkout page for a reference
// created by POST /subscription/checkout (F008).
//
// The browser (opened by the Flutter CTA via launchUrl) lands here.
// The handler:
//   - looks up the SubscriptionCheckout row for :ref — unknown → 404
//     (caso borde §9: a checkout that does not exist cannot be served),
//   - rebuilds the ePayco widget params from that row,
//   - returns an HTML page that loads checkout.js and opens the widget.
//
// PUBLIC: no JWT — the browser tab opened from the app carries no
// token. The reference is unguessable (tenant id + nanosecond + uuid),
// and the page exposes only the PUBLIC ePayco key, never the private
// credentials. Without ePayco credentials it responds 503, mirroring
// /subscription/checkout — the page literally cannot arm the widget.
func SubscriptionPayPage(db *gorm.DB, epayco *services.EpaycoService) gin.HandlerFunc {
	return func(c *gin.Context) {
		ref := strings.TrimSpace(c.Param("ref"))
		if ref == "" {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "referencia de pago no encontrada",
			})
			return
		}

		var row models.SubscriptionCheckout
		err := db.Where("reference = ?", ref).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "referencia de pago no encontrada",
			})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo cargar el checkout",
			})
			return
		}

		if !epayco.IsConfigured() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "la pasarela de pagos no está disponible por ahora",
			})
			return
		}

		// Reconstruir los params del widget desde la fila persistida. Se
		// usa BuildCheckout para que la página comparta exactamente la
		// misma forma que produce el resto de la feature.
		checkout := epayco.BuildCheckout(services.CheckoutParams{
			TenantID: row.TenantID,
			Plan:     row.Plan,
			Interval: row.Interval,
			Price: billing.Price{
				Interval: row.Interval,
				Amount:   row.Amount,
				Currency: "COP",
			},
			Reference:       row.Reference,
			ResponseURL:     row.ResponseURL,
			ConfirmationURL: row.ConfirmationURL,
		})

		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, epayco.RenderCheckoutPage(checkout))
	}
}

// publicBaseURL derives the externally-reachable base URL of the API
// from the request. ePayco needs absolute URLs for the response /
// confirmation callbacks. Honours X-Forwarded-Proto so Render's TLS
// terminator (HTTP internally) still yields an https:// callback.
func publicBaseURL(c *gin.Context) string {
	scheme := "https"
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if c.Request.TLS == nil && strings.HasPrefix(c.Request.Host, "localhost") {
		scheme = "http"
	}
	return scheme + "://" + c.Request.Host
}

// ── POST /api/v1/subscription/epayco/confirmation ───────────────────

// EpaycoConfirmation is the PUBLIC ePayco webhook (FR-05). It is the
// single source of truth for promoting a tenant to PRO (D2).
//
// Security & correctness contract:
//   - The endpoint is public, so the SHA-256 signature is verified
//     before anything is trusted. Invalid signature → 400, no promote
//     (AC-06).
//   - Only an accepted transaction (x_cod_response == 1) promotes the
//     tenant. Rejected/pending is acknowledged with 200 but changes
//     nothing.
//   - Idempotent on epayco_transaction_id (Art. II / AC-07): the
//     payment row carries a UNIQUE index on that column, so a re-sent
//     confirmation fails the insert inside the transaction, the whole
//     transaction rolls back, and the tenant is NOT re-promoted nor
//     the period extended twice. A duplicate is acknowledged 200.
//
// ePayco always expects a 200 for deliveries it should stop retrying;
// we return 400 only for genuinely bad input (forged signature, no
// tenant) so ePayco surfaces it in its dashboard.
func EpaycoConfirmation(db *gorm.DB, epayco *services.EpaycoService) gin.HandlerFunc {
	return func(c *gin.Context) {
		conf := parseEpaycoForm(c)

		// 1. Verificación de firma — obligatoria (Art. VI / AC-06).
		if !epayco.VerifySignature(conf) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "firma de confirmación inválida",
			})
			return
		}

		// 2. Reconciliación: extra1 lleva el tenant_id que pusimos en el
		//    checkout. Sin él no podemos saber a quién promover.
		tenantID := strings.TrimSpace(conf.Extra1)
		if tenantID == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "confirmación sin tenant asociado",
			})
			return
		}

		// 3. Transacción no aceptada → acuse 200 sin promover.
		if !conf.IsAccepted() {
			c.JSON(http.StatusOK, gin.H{
				"message": "confirmación recibida — transacción no aceptada",
				"status":  conf.CodResponse,
			})
			return
		}

		// 4. La idempotencia (AC-07) depende del transaction id: sin él
		//    no podemos distinguir un reenvío de un pago nuevo. Una
		//    confirmación aceptada sin x_transaction_id se rechaza.
		txID := strings.TrimSpace(conf.TransactionID)
		if txID == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "confirmación sin identificador de transacción",
			})
			return
		}

		// 5. Resolver plan/interval (extra2/extra3). Si vienen vacíos o
		//    inválidos, default a PRO mensual — una transacción aceptada
		//    sin metadatos sigue siendo dinero recibido.
		plan := conf.Extra2
		interval := conf.Extra3
		if !billing.IsValidPlan(plan) || plan == billing.PlanFree {
			plan = billing.PlanPro
		}
		if !billing.IsValidInterval(interval) {
			interval = billing.IntervalMonthly
		}

		amount := parseAmount(conf.Amount)
		now := time.Now()

		// 6. Escritura atómica: insertar el pago + promover. El insert
		//    del pago choca con el índice UNIQUE de
		//    epayco_transaction_id si la confirmación ya se procesó —
		//    eso aborta la transacción y la promoción NO se repite
		//    (idempotencia, AC-07).
		txErr := db.Transaction(func(tx *gorm.DB) error {
			payment := models.SubscriptionPayment{
				TenantID:    tenantID,
				Status:      models.SubscriptionPaymentStatusConfirmed,
				ExternalRef: conf.RefPayco,
				Amount:      amount,
				Currency:    defaultCurrency(conf.CurrencyCode),
				Plan:        plan,
				Interval:    interval,
				EpaycoRef:   conf.RefPayco,
				// *string so the UNIQUE index ignores NULL legacy rows;
				// a real id here is what the idempotency check hits.
				EpaycoTransactionID: &txID,
				PaidAt:              &now,
				ConfirmedAt:         &now,
			}
			if err := tx.Create(&payment).Error; err != nil {
				// Choque con el índice único = confirmación duplicada.
				return err
			}

			// Promover el tenant a PRO_ACTIVE. La renovación extiende
			// desde el vencimiento vigente si todavía no pasó (caso
			// borde del spec §9); si ya venció, desde ahora.
			var sub models.TenantSubscription
			subErr := tx.Where("tenant_id = ?", tenantID).First(&sub).Error
			periodStart := now
			if subErr == nil && sub.CurrentPeriodEnd != nil &&
				sub.CurrentPeriodEnd.After(now) {
				periodStart = *sub.CurrentPeriodEnd
			}
			periodEnd := billing.ExtendPeriod(periodStart, interval)

			updates := map[string]any{
				"status":             models.SubscriptionStatusProActive,
				"plan":               plan,
				"interval":           interval,
				"current_period_end": periodEnd,
				"updated_at":         now,
			}
			if errors.Is(subErr, gorm.ErrRecordNotFound) {
				// El tenant no tenía fila — crearla ya promovida.
				return tx.Create(&models.TenantSubscription{
					TenantID:         tenantID,
					Status:           models.SubscriptionStatusProActive,
					Plan:             plan,
					Interval:         interval,
					CurrentPeriodEnd: &periodEnd,
				}).Error
			}
			if subErr != nil {
				return subErr
			}
			return tx.Model(&models.TenantSubscription{}).
				Where("tenant_id = ?", tenantID).
				Updates(updates).Error
		})

		if txErr != nil {
			// Idempotencia (AC-07): un choque con el índice único de
			// epayco_transaction_id significa que esta confirmación ya
			// se procesó. No es un error — se acusa 200 sin reprocesar.
			if isDuplicateConfirmation(db, txID) {
				c.JSON(http.StatusOK, gin.H{
					"message": "confirmación ya procesada",
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al procesar la confirmación",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "pago confirmado — suscripción activada",
		})
	}
}

// parseEpaycoForm extracts the ePayco x_* fields from the request.
// ePayco posts application/x-www-form-urlencoded; PostForm covers that
// and Gin's ShouldBind would too, but reading explicitly keeps the
// field mapping auditable.
func parseEpaycoForm(c *gin.Context) services.EpaycoConfirmation {
	return services.EpaycoConfirmation{
		RefPayco:      c.PostForm("x_ref_payco"),
		TransactionID: c.PostForm("x_transaction_id"),
		Amount:        c.PostForm("x_amount"),
		CurrencyCode:  c.PostForm("x_currency_code"),
		Signature:     c.PostForm("x_signature"),
		CodResponse:   c.PostForm("x_cod_response"),
		ResponseText:  c.PostForm("x_response_reason_text"),
		Invoice:       c.PostForm("x_id_invoice"),
		Extra1:        c.PostForm("x_extra1"),
		Extra2:        c.PostForm("x_extra2"),
		Extra3:        c.PostForm("x_extra3"),
	}
}

// isDuplicateConfirmation reports whether a payment row already exists
// for the given ePayco transaction id — used to tell an idempotent
// re-delivery apart from a genuine DB error.
func isDuplicateConfirmation(db *gorm.DB, txID string) bool {
	if txID == "" {
		return false
	}
	var count int64
	db.Model(&models.SubscriptionPayment{}).
		Where("epayco_transaction_id = ?", txID).
		Count(&count)
	return count > 0
}

// parseAmount converts ePayco's amount string ("29900" or "29900.00")
// to a float. A bad value yields 0 — the payment is still recorded so
// the transaction is not lost; FinOps surfaces a 0-amount row for ops.
func parseAmount(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// defaultCurrency falls back to COP when ePayco omits the currency.
func defaultCurrency(code string) string {
	if strings.TrimSpace(code) == "" {
		return "COP"
	}
	return strings.ToUpper(code)
}

// ── GET /api/v1/subscription/response ───────────────────────────────

// SubscriptionResponse is the PUBLIC landing the browser is redirected
// to after the ePayco checkout closes (UX only — it decides NOTHING,
// the webhook is the source of truth, D2). It renders a small Spanish
// HTML page telling the merchant the payment is being confirmed.
func SubscriptionResponse() gin.HandlerFunc {
	const page = `<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>VendIA — Pago recibido</title>
<style>
body{font-family:system-ui,sans-serif;background:#f4f5f7;margin:0;
display:flex;align-items:center;justify-content:center;min-height:100vh}
.card{background:#fff;border-radius:16px;padding:32px;max-width:340px;
text-align:center;box-shadow:0 8px 24px rgba(0,0,0,.08)}
h1{font-size:22px;color:#1a7f37;margin:8px 0}
p{font-size:16px;color:#444;line-height:1.5}
</style>
</head>
<body>
<div class="card">
<h1>¡Gracias por tu pago!</h1>
<p>Estamos confirmando tu suscripción. En unos segundos tu plan Pro
quedará activo. Ya puedes volver a la app.</p>
</div>
</body>
</html>`
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, page)
	}
}
