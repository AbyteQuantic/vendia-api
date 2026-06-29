// Spec: specs/031-cotizaciones/spec.md
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpireQuotesJob_AuthAndRun covers the CRON_TOKEN gate of the
// internal job endpoint (Spec F031 T-22):
//   - no CRON_TOKEN configured  → 503 (fail closed)
//   - wrong / missing Bearer    → 401
//   - correct Bearer            → 200, runs the job
// Spec 093/091 — el monitoreo de capacidad respeta el gate CRON_TOKEN. La
// consulta pg_database_size es de Postgres (se verifica en prod, Art. XII); aquí
// solo se prueba el candado de auth con sqlite.
func TestCapacityCheckJob_AuthGate(t *testing.T) {
	db := setupQuoteDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/jobs/capacity-check", handlers.CapacityCheckJob(db))
	call := func(h string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost,
			"/api/v1/internal/jobs/capacity-check", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		r.ServeHTTP(w, req)
		return w
	}
	t.Setenv("CRON_TOKEN", "")
	assert.Equal(t, http.StatusServiceUnavailable, call("Bearer x").Code,
		"sin CRON_TOKEN → 503 fail-closed")
	t.Setenv("CRON_TOKEN", "tok")
	assert.Equal(t, http.StatusUnauthorized, call("Bearer wrong").Code,
		"token incorrecto → 401")
}

func TestExpireQuotesJob_AuthAndRun(t *testing.T) {
	db := setupQuoteDB(t)

	// Seed one expired-eligible quote so a successful run reports work.
	require.NoError(t, db.Create(&models.Quote{
		TenantID:    "cccccccc-cccc-cccc-cccc-cccccccccccc",
		CustomerID:  "dddddddd-dddd-dddd-dddd-dddddddddddd",
		Folio:       "COT-2026-9001",
		Status:      models.QuoteStatusSent,
		ValidUntil:  time.Now().Add(-2 * time.Hour),
		PublicToken: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
	}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/jobs/expire-quotes", handlers.ExpireQuotesJob(db))

	call := func(authHeader string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost,
			"/api/v1/internal/jobs/expire-quotes", nil)
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		r.ServeHTTP(w, req)
		return w
	}

	// ── CRON_TOKEN unset → fail closed (503) ───────────────────────────
	t.Setenv("CRON_TOKEN", "")
	assert.Equal(t, http.StatusServiceUnavailable, call("Bearer anything").Code,
		"sin CRON_TOKEN el endpoint debe fallar cerrado (503)")

	// ── CRON_TOKEN set, wrong token → 401 ──────────────────────────────
	t.Setenv("CRON_TOKEN", "s3cr3t-cron-token")
	assert.Equal(t, http.StatusUnauthorized, call("Bearer wrong").Code,
		"token de cron incorrecto → 401")
	assert.Equal(t, http.StatusUnauthorized, call("").Code,
		"sin header Authorization → 401")

	// ── correct token → 200, job ran ───────────────────────────────────
	w := call("Bearer s3cr3t-cron-token")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"expired":1`,
		"el job debe reportar 1 cotización vencida")

	// The seeded quote is now `vencida`.
	var stored models.Quote
	require.NoError(t, db.Where("folio = ?", "COT-2026-9001").First(&stored).Error)
	assert.Equal(t, models.QuoteStatusExpired, stored.Status)
}

// TestPromotionsPushJob_AuthAndRun covers the CRON_TOKEN gate of the
// F033 promotions-push internal endpoint:
//   - no CRON_TOKEN configured → 503 (fail closed)
//   - wrong / missing Bearer   → 401
//   - correct Bearer           → 200, runs the job and notifies the owner
func TestPromotionsPushJob_AuthAndRun(t *testing.T) {
	db := setupPromoDB(t)

	// Seed a scheduled promotion whose send time has already arrived so
	// a successful run reports work.
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	require.NoError(t, db.Create(&models.BroadcastPromotion{
		BaseModel:    models.BaseModel{ID: "11111111-1111-1111-1111-111111111111"},
		TenantID:     "cccccccc-cccc-cccc-cccc-cccccccccccc",
		Title:        "Promo programada",
		ValidFrom:    now,
		ValidUntil:   now.AddDate(0, 0, 7),
		PublicToken:  "22222222-2222-2222-2222-222222222222",
		ScheduledFor: &past,
	}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/jobs/promotions-push", handlers.PromotionsPushJob(db, nil))

	call := func(authHeader string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost,
			"/api/v1/internal/jobs/promotions-push", nil)
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		r.ServeHTTP(w, req)
		return w
	}

	t.Setenv("CRON_TOKEN", "")
	assert.Equal(t, http.StatusServiceUnavailable, call("Bearer anything").Code,
		"sin CRON_TOKEN el endpoint debe fallar cerrado (503)")

	t.Setenv("CRON_TOKEN", "s3cr3t-cron-token")
	assert.Equal(t, http.StatusUnauthorized, call("Bearer wrong").Code,
		"token de cron incorrecto → 401")
	assert.Equal(t, http.StatusUnauthorized, call("").Code,
		"sin header Authorization → 401")

	w := call("Bearer s3cr3t-cron-token")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"notified":1`,
		"el job debe reportar 1 promoción notificada")

	var stored models.BroadcastPromotion
	require.NoError(t, db.First(&stored, "id = ?", "11111111-1111-1111-1111-111111111111").Error)
	assert.True(t, stored.SchedulePushSent, "la promo notificada queda marcada")
}
