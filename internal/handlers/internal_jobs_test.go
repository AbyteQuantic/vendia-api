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
