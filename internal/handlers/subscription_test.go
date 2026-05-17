// Spec: specs/008-planes-suscripcion-epayco/spec.md
package handlers_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"vendia-backend/internal/billing"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── Test fixtures ───────────────────────────────────────────────────

const (
	subTestPCustID = "1234567"
	subTestPKey    = "p_key_secret_abc"
)

func setupSubscriptionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.TenantSubscription{},
		&models.SubscriptionPayment{},
		&models.SubscriptionCheckout{},
	))
	return db
}

func subTestEpayco() *services.EpaycoService {
	return services.NewEpaycoService(services.EpaycoConfig{
		PublicKey:  "pub_test",
		PrivateKey: "priv_test",
		PCustID:    subTestPCustID,
		PKey:       subTestPKey,
		TestMode:   true,
	})
}

// mountSubscriptionRoutes wires the JWT-scoped subscription endpoints
// with a fake auth middleware that injects tenantID into context.
func mountSubscriptionRoutes(db *gorm.DB, ep *services.EpaycoService, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	authed := r.Group("/api/v1")
	authed.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	authed.GET("/subscription/plans", handlers.GetSubscriptionPlans())
	authed.GET("/subscription/status", handlers.GetSubscriptionStatus(db))
	authed.POST("/subscription/checkout", handlers.CreateSubscriptionCheckout(db, ep))
	// Public — no auth group.
	r.POST("/api/v1/subscription/epayco/confirmation",
		handlers.EpaycoConfirmation(db, ep))
	r.GET("/api/v1/subscription/response", handlers.SubscriptionResponse())
	// /pay/:ref is PUBLIC — the browser opens it after /checkout.
	r.GET("/api/v1/subscription/pay/:ref", handlers.SubscriptionPayPage(db, ep))
	return r
}

// signConfirmation builds the SHA-256 x_signature ePayco would send.
func signConfirmation(refPayco, txID, amount, currency string) string {
	raw := strings.Join([]string{
		subTestPCustID, subTestPKey, refPayco, txID, amount, currency,
	}, "^")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// postConfirmation posts a form-encoded ePayco confirmation.
func postConfirmation(r *gin.Engine, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/subscription/epayco/confirmation",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	return w
}

// acceptedConfirmationForm builds a valid, accepted ePayco confirmation
// for a PRO monthly purchase by tenantID.
func acceptedConfirmationForm(tenantID, txID string) url.Values {
	refPayco := "epayco-ref-" + txID
	amount := "29900"
	form := url.Values{}
	form.Set("x_ref_payco", refPayco)
	form.Set("x_transaction_id", txID)
	form.Set("x_amount", amount)
	form.Set("x_currency_code", "COP")
	form.Set("x_cod_response", "1") // aceptada
	form.Set("x_response_reason_text", "Aprobada")
	form.Set("x_id_invoice", "vendia-sub-"+tenantID+"-001")
	form.Set("x_extra1", tenantID)
	form.Set("x_extra2", billing.PlanPro)
	form.Set("x_extra3", billing.IntervalMonthly)
	form.Set("x_signature", signConfirmation(refPayco, txID, amount, "COP"))
	return form
}

// ── GET /subscription/plans (AC-03) ─────────────────────────────────

func TestGetSubscriptionPlans_ReturnsFreeAndPro(t *testing.T) {
	r := mountSubscriptionRoutes(setupSubscriptionDB(t), subTestEpayco(), uuid.NewString())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subscription/plans", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data []billing.Plan `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2)
	assert.Equal(t, billing.PlanFree, resp.Data[0].ID)
	assert.Equal(t, billing.PlanPro, resp.Data[1].ID)
	// Pro monthly price visible.
	var proMonthly int
	for _, p := range resp.Data[1].Prices {
		if p.Interval == billing.IntervalMonthly {
			proMonthly = p.Amount
		}
	}
	assert.Equal(t, 29900, proMonthly)
}

// ── GET /subscription/status (AC-08) ────────────────────────────────

func TestGetSubscriptionStatus_ActiveTrial(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	ends := time.Now().Add(10 * 24 * time.Hour)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusTrial,
		Plan: models.SubscriptionPlanFree, TrialEndsAt: &ends,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subscription/status", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Status             string `json:"status"`
		IsPremium          bool   `json:"is_premium"`
		TrialDaysRemaining int    `json:"trial_days_remaining"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.SubscriptionStatusTrial, resp.Status)
	assert.True(t, resp.IsPremium)
	assert.Equal(t, 10, resp.TrialDaysRemaining)
}

func TestGetSubscriptionStatus_ExpiredTrialDegradesToFree(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	ends := time.Now().Add(-1 * time.Hour) // vencido
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusTrial,
		Plan: models.SubscriptionPlanFree, TrialEndsAt: &ends,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subscription/status", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Status    string `json:"status"`
		IsPremium bool   `json:"is_premium"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.SubscriptionStatusFree, resp.Status,
		"un TRIAL vencido se reporta como FREE (AC-08)")
	assert.False(t, resp.IsPremium)

	// Write-through: the stored row is degraded so the dashboard flips.
	var stored models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&stored).Error)
	assert.Equal(t, models.SubscriptionStatusFree, stored.Status)
}

func TestGetSubscriptionStatus_ExpiredProDegradesToFree(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	periodEnd := time.Now().Add(-1 * time.Hour)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusProActive,
		Plan: models.SubscriptionPlanPro, CurrentPeriodEnd: &periodEnd,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subscription/status", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.SubscriptionStatusFree, resp.Status,
		"un PRO vencido se reporta como FREE")
}

func TestGetSubscriptionStatus_NoRowReadsAsFree(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subscription/status", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Status    string `json:"status"`
		IsPremium bool   `json:"is_premium"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.SubscriptionStatusFree, resp.Status)
	assert.False(t, resp.IsPremium)
}

func TestGetSubscriptionStatus_RequiresTenant(t *testing.T) {
	r := mountSubscriptionRoutes(setupSubscriptionDB(t), subTestEpayco(), "")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subscription/status", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ── POST /subscription/checkout (AC-04) ─────────────────────────────
//
// F008 reconciliation: /checkout no longer hands the raw ePayco widget
// params to the Flutter client. It persists the checkout and returns a
// checkout_url — the URL of the backend-served page that opens the
// ePayco widget. The Flutter CTA opens that URL with launchUrl.

// checkoutResponse is the new contract of POST /subscription/checkout.
type checkoutResponse struct {
	Data struct {
		CheckoutURL string `json:"checkout_url"`
		Reference   string `json:"reference"`
		Amount      int    `json:"amount"`
		Plan        string `json:"plan"`
		Interval    string `json:"interval"`
	} `json:"data"`
}

func postCheckout(r *gin.Engine, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/subscription/checkout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestCreateCheckout_ReturnsCheckoutURLAndReference(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)

	w := postCheckout(r, `{"plan":"PRO","interval":"monthly"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp checkoutResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// El contrato que el Flutter espera: una URL para abrir con launchUrl.
	assert.NotEmpty(t, resp.Data.CheckoutURL, "checkout_url requerida (F008)")
	assert.Contains(t, resp.Data.CheckoutURL, "/api/v1/subscription/pay/",
		"checkout_url apunta a la página de pago servida por el backend")
	assert.NotEmpty(t, resp.Data.Reference, "referencia única requerida")
	assert.True(t, strings.HasPrefix(resp.Data.Reference, "vendia-sub-"))
	// La URL termina con la referencia.
	assert.True(t, strings.HasSuffix(resp.Data.CheckoutURL, resp.Data.Reference),
		"la URL incluye la referencia como segmento de ruta")
	assert.Equal(t, 29900, resp.Data.Amount, "Pro mensual = 29.900 COP (AC-04)")
	assert.Equal(t, billing.PlanPro, resp.Data.Plan)
	assert.Equal(t, billing.IntervalMonthly, resp.Data.Interval)
}

func TestCreateCheckout_PersistsCheckoutRow(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)

	w := postCheckout(r, `{"plan":"PRO","interval":"yearly"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp checkoutResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// La referencia se persiste para que /pay/:ref pueda servir la página.
	var stored models.SubscriptionCheckout
	require.NoError(t,
		db.Where("reference = ?", resp.Data.Reference).First(&stored).Error,
		"el checkout se persiste para servir /pay/:ref")
	assert.Equal(t, tenantID, stored.TenantID)
	assert.Equal(t, billing.PlanPro, stored.Plan)
	assert.Equal(t, billing.IntervalYearly, stored.Interval)
	assert.Equal(t, 299000, stored.Amount, "Pro anual = 299.000 COP")
	assert.Contains(t, stored.ConfirmationURL, "/subscription/epayco/confirmation")
	assert.Contains(t, stored.ResponseURL, "/subscription/response")
}

func TestCreateCheckout_UniqueReferences(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)

	doCheckout := func() string {
		w := postCheckout(r, `{"plan":"PRO","interval":"yearly"}`)
		require.Equal(t, http.StatusOK, w.Code)
		var resp checkoutResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		return resp.Data.Reference
	}
	assert.NotEqual(t, doCheckout(), doCheckout(), "cada checkout genera una ref única")
}

func TestCreateCheckout_RejectsInvalidPlan(t *testing.T) {
	db := setupSubscriptionDB(t)
	r := mountSubscriptionRoutes(db, subTestEpayco(), uuid.NewString())

	w := postCheckout(r, `{"plan":"ENTERPRISE","interval":"monthly"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateCheckout_RejectsFreePlan(t *testing.T) {
	db := setupSubscriptionDB(t)
	r := mountSubscriptionRoutes(db, subTestEpayco(), uuid.NewString())

	w := postCheckout(r, `{"plan":"FREE","interval":"monthly"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"el plan Gratis no se cobra — no hay checkout")
}

func TestCreateCheckout_UnconfiguredGatewayFails(t *testing.T) {
	db := setupSubscriptionDB(t)
	unconfigured := services.NewEpaycoService(services.EpaycoConfig{})
	r := mountSubscriptionRoutes(db, unconfigured, uuid.NewString())

	w := postCheckout(r, `{"plan":"PRO","interval":"monthly"}`)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code,
		"sin credenciales ePayco el checkout no puede armarse")
}

// ── GET /subscription/pay/:ref (F008 — página de checkout servida) ──

func TestSubscriptionPayPage_ValidRefServesWidgetHTML(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)

	// Un checkout real crea la referencia.
	wc := postCheckout(r, `{"plan":"PRO","interval":"monthly"}`)
	require.Equal(t, http.StatusOK, wc.Code, wc.Body.String())
	var resp checkoutResponse
	require.NoError(t, json.Unmarshal(wc.Body.Bytes(), &resp))

	// La página de pago se sirve para esa referencia.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/subscription/pay/"+resp.Data.Reference, nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	body := w.Body.String()
	assert.Contains(t, body, "https://checkout.epayco.co/checkout.js",
		"la página carga el widget oficial de ePayco")
	assert.Contains(t, body, "ePayco.checkout.configure",
		"la página configura y abre el widget")
	assert.Contains(t, body, resp.Data.Reference,
		"el widget usa la referencia como invoice")
	assert.Contains(t, body, "29900", "el widget cobra el monto del plan")
	assert.Contains(t, body, tenantID, "extra1 lleva el tenant_id")
}

func TestSubscriptionPayPage_UnknownRefIs404(t *testing.T) {
	db := setupSubscriptionDB(t)
	r := mountSubscriptionRoutes(db, subTestEpayco(), uuid.NewString())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/subscription/pay/ref-que-no-existe", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code,
		"una referencia desconocida → 404 (caso borde §9)")
}

func TestSubscriptionPayPage_UnconfiguredGatewayIs503(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	// La referencia existe (se sembró con un gateway configurado)...
	require.NoError(t, db.Create(&models.SubscriptionCheckout{
		TenantID: tenantID, Reference: "vendia-sub-seed-001",
		Plan: billing.PlanPro, Interval: billing.IntervalMonthly,
		Amount: 29900, Description: "VendIA Pro",
	}).Error)

	// ...pero la página se sirve con un gateway SIN credenciales.
	unconfigured := services.NewEpaycoService(services.EpaycoConfig{})
	r := mountSubscriptionRoutes(db, unconfigured, tenantID)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/subscription/pay/vendia-sub-seed-001", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code,
		"sin credenciales ePayco la página no puede armar el widget")
}

// ── POST /subscription/epayco/confirmation ──────────────────────────

// AC-05: firma válida + transacción aceptada → PRO_ACTIVE + payment.
func TestEpaycoConfirmation_ValidAcceptedPromotesToPro(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-" + uuid.NewString()[:8]
	w := postConfirmation(r, acceptedConfirmationForm(tenantID, txID))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	assert.Equal(t, models.SubscriptionStatusProActive, sub.Status,
		"firma válida + aceptada → PRO_ACTIVE (AC-05)")
	assert.Equal(t, models.SubscriptionPlanPro, sub.Plan)
	assert.Equal(t, billing.IntervalMonthly, sub.Interval)
	require.NotNil(t, sub.CurrentPeriodEnd, "el periodo pagado debe vencer")
	assert.True(t, sub.CurrentPeriodEnd.After(time.Now().Add(27*24*time.Hour)),
		"mensual extiende ~1 mes")

	var pay models.SubscriptionPayment
	require.NoError(t,
		db.Where("epayco_transaction_id = ?", txID).First(&pay).Error,
		"se escribe un subscription_payments (AC-05)")
	assert.Equal(t, models.SubscriptionPaymentStatusConfirmed, pay.Status)
	assert.Equal(t, float64(29900), pay.Amount)
	assert.Equal(t, "COP", pay.Currency)
	assert.Equal(t, tenantID, pay.TenantID)
	require.NotNil(t, pay.PaidAt)
}

func TestEpaycoConfirmation_YearlyExtendsOneYear(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-year-" + uuid.NewString()[:8]
	form := acceptedConfirmationForm(tenantID, txID)
	form.Set("x_amount", "299000")
	form.Set("x_extra3", billing.IntervalYearly)
	form.Set("x_signature", signConfirmation(
		form.Get("x_ref_payco"), txID, "299000", "COP"))

	w := postConfirmation(r, form)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	require.NotNil(t, sub.CurrentPeriodEnd)
	assert.True(t, sub.CurrentPeriodEnd.After(time.Now().Add(360*24*time.Hour)),
		"anual extiende ~1 año")
}

// AC-06: firma inválida → rechaza, NO promueve.
func TestEpaycoConfirmation_InvalidSignatureRejected(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-forged-" + uuid.NewString()[:8]
	form := acceptedConfirmationForm(tenantID, txID)
	form.Set("x_signature", "deadbeefdeadbeef") // firma falsa

	w := postConfirmation(r, form)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"firma inválida → rechazo (AC-06)")

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	assert.Equal(t, models.SubscriptionStatusFree, sub.Status,
		"firma inválida NO promueve el tenant (AC-06)")

	var count int64
	db.Model(&models.SubscriptionPayment{}).
		Where("epayco_transaction_id = ?", txID).Count(&count)
	assert.Zero(t, count, "firma inválida NO escribe pago")
}

// AC-07: confirmación duplicada → idempotente.
func TestEpaycoConfirmation_DuplicateIsIdempotent(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-dup-" + uuid.NewString()[:8]
	form := acceptedConfirmationForm(tenantID, txID)

	// Primera confirmación.
	w1 := postConfirmation(r, form)
	require.Equal(t, http.StatusOK, w1.Code, w1.Body.String())

	// ePayco reenvía la MISMA confirmación.
	w2 := postConfirmation(r, form)
	assert.Equal(t, http.StatusOK, w2.Code,
		"reenvío de confirmación responde OK sin error")

	// Solo un pago.
	var payCount int64
	db.Model(&models.SubscriptionPayment{}).
		Where("epayco_transaction_id = ?", txID).Count(&payCount)
	assert.Equal(t, int64(1), payCount, "no se duplica el pago (AC-07)")

	// El periodo no se extendió dos veces.
	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	require.NotNil(t, sub.CurrentPeriodEnd)
	assert.True(t, sub.CurrentPeriodEnd.Before(time.Now().Add(40*24*time.Hour)),
		"reprocesar NO extiende el periodo dos veces (AC-07)")
}

func TestEpaycoConfirmation_RejectedTransactionNoPromote(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-rej-" + uuid.NewString()[:8]
	form := acceptedConfirmationForm(tenantID, txID)
	form.Set("x_cod_response", "2") // rechazada
	// firma sigue válida (la firma no incluye cod_response).

	w := postConfirmation(r, form)
	assert.Equal(t, http.StatusOK, w.Code,
		"una transacción rechazada se acepta el webhook pero no promueve")

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	assert.Equal(t, models.SubscriptionStatusFree, sub.Status,
		"transacción rechazada NO promueve")
}

func TestEpaycoConfirmation_RenewalExtendsExistingPeriod(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	// Tenant ya PRO con periodo a 5 días.
	currentEnd := time.Now().Add(5 * 24 * time.Hour)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusProActive,
		Plan: models.SubscriptionPlanPro, Interval: billing.IntervalMonthly,
		CurrentPeriodEnd: &currentEnd,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-renew-" + uuid.NewString()[:8]
	w := postConfirmation(r, acceptedConfirmationForm(tenantID, txID))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	require.NotNil(t, sub.CurrentPeriodEnd)
	// Renovación extiende desde el vencimiento actual, no desde hoy.
	assert.True(t, sub.CurrentPeriodEnd.After(time.Now().Add(33*24*time.Hour)),
		"renovación extiende desde el periodo vigente (caso borde)")
}

func TestEpaycoConfirmation_UnknownTenantDoesNotCrash(t *testing.T) {
	db := setupSubscriptionDB(t)
	r := mountSubscriptionRoutes(db, subTestEpayco(), "")
	txID := "tx-ghost-" + uuid.NewString()[:8]
	// extra1 apunta a un tenant que no existe.
	form := acceptedConfirmationForm("tenant-inexistente", txID)

	w := postConfirmation(r, form)
	// No revienta — responde sin 500.
	assert.NotEqual(t, http.StatusInternalServerError, w.Code, w.Body.String())
}

func TestEpaycoConfirmation_MissingTenantRef(t *testing.T) {
	db := setupSubscriptionDB(t)
	r := mountSubscriptionRoutes(db, subTestEpayco(), "")
	txID := "tx-noext-" + uuid.NewString()[:8]
	form := acceptedConfirmationForm("", txID) // extra1 vacío
	form.Set("x_extra1", "")
	form.Set("x_signature", signConfirmation(
		form.Get("x_ref_payco"), txID, form.Get("x_amount"), "COP"))

	w := postConfirmation(r, form)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"sin tenant en extra1 la confirmación no se puede reconciliar")
}

// Cubre los fallbacks defensivos: monto no numérico → 0, moneda
// ausente → COP. La transacción aceptada se registra igual (no se
// pierde el dinero) — FinOps ve una fila para reconciliar.
func TestEpaycoConfirmation_GarbageAmountAndCurrencyFallback(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-garbage-" + uuid.NewString()[:8]
	refPayco := "epayco-ref-" + txID
	form := url.Values{}
	form.Set("x_ref_payco", refPayco)
	form.Set("x_transaction_id", txID)
	form.Set("x_amount", "no-es-un-numero")
	form.Set("x_currency_code", "") // moneda ausente
	form.Set("x_cod_response", "1")
	form.Set("x_extra1", tenantID)
	form.Set("x_extra2", billing.PlanPro)
	form.Set("x_extra3", billing.IntervalMonthly)
	// La firma se calcula sobre los valores tal cual ePayco los envía.
	form.Set("x_signature", signConfirmation(refPayco, txID, "no-es-un-numero", ""))

	w := postConfirmation(r, form)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var pay models.SubscriptionPayment
	require.NoError(t, db.Where("epayco_transaction_id = ?", txID).First(&pay).Error)
	assert.Equal(t, float64(0), pay.Amount, "monto no numérico → 0")
	assert.Equal(t, "COP", pay.Currency, "moneda ausente → COP")
}

// Una transacción aceptada con extra2/extra3 vacíos cae al default
// PRO mensual — sigue siendo dinero recibido.
func TestEpaycoConfirmation_MissingPlanMetadataDefaultsToProMonthly(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	txID := "tx-nometa-" + uuid.NewString()[:8]
	form := acceptedConfirmationForm(tenantID, txID)
	form.Set("x_extra2", "")
	form.Set("x_extra3", "")

	w := postConfirmation(r, form)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	assert.Equal(t, models.SubscriptionPlanPro, sub.Plan)
	assert.Equal(t, billing.IntervalMonthly, sub.Interval)
}

// Una confirmación aceptada sin x_transaction_id no puede hacerse
// idempotente — se rechaza con 400.
func TestEpaycoConfirmation_MissingTransactionIDRejected(t *testing.T) {
	db := setupSubscriptionDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID, Status: models.SubscriptionStatusFree,
		Plan: models.SubscriptionPlanFree,
	}).Error)

	r := mountSubscriptionRoutes(db, subTestEpayco(), tenantID)
	refPayco := "epayco-ref-noid"
	form := url.Values{}
	form.Set("x_ref_payco", refPayco)
	form.Set("x_transaction_id", "") // sin id
	form.Set("x_amount", "29900")
	form.Set("x_currency_code", "COP")
	form.Set("x_cod_response", "1")
	form.Set("x_extra1", tenantID)
	form.Set("x_extra2", billing.PlanPro)
	form.Set("x_extra3", billing.IntervalMonthly)
	form.Set("x_signature", signConfirmation(refPayco, "", "29900", "COP"))

	w := postConfirmation(r, form)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	assert.Equal(t, models.SubscriptionStatusFree, sub.Status,
		"sin transaction id no se promueve")
}

// ── GET /subscription/response ──────────────────────────────────────

func TestSubscriptionResponse_RendersLanding(t *testing.T) {
	r := mountSubscriptionRoutes(setupSubscriptionDB(t), subTestEpayco(), "")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/subscription/response?ref_payco=abc", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
