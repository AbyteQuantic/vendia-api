// Spec: specs/031-cotizaciones/spec.md
package handlers

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
	"time"

	"vendia-backend/internal/jobs"
	"vendia-backend/internal/services/push"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// cronAuthOK enforces the shared-secret Bearer gate every internal job
// endpoint relies on. It writes the appropriate error response and
// returns false when the request must be rejected:
//
//   - CRON_TOKEN unset    → 503 (fail closed; a misconfigured deploy
//     must never run an internal job unauthenticated).
//   - token missing/wrong → 401.
//
// The compare is constant-time so the secret cannot be probed by timing.
func cronAuthOK(c *gin.Context) bool {
	expected := strings.TrimSpace(os.Getenv("CRON_TOKEN"))
	if expected == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "endpoint de cron no configurado",
		})
		return false
	}
	provided := bearerToken(c.GetHeader("Authorization"))
	if provided == "" ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token de cron inválido"})
		return false
	}
	return true
}

// ExpireQuotesJob is the internal cron endpoint that runs the
// expire-quotes batch job (Spec F031 AC-10, plan §4). It is NOT behind
// JWT — Render Cron Jobs carry no tenant token — but it IS gated by a
// shared Bearer secret read from the CRON_TOKEN environment variable.
//
// When CRON_TOKEN is unset the endpoint refuses every request (503): a
// misconfigured deploy must fail closed, never run unauthenticated.
// POST /api/v1/internal/jobs/expire-quotes
func ExpireQuotesJob(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}

		expired, err := jobs.ExpireQuotes(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo ejecutar el job de expiración",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"expired": expired})
	}
}

// PromotionsPushJob is the internal cron endpoint that runs the
// promotions-push batch job (Spec F033 §4.5 #5, AC-06d). Same auth
// model as ExpireQuotesJob: no JWT, gated by the shared CRON_TOKEN
// Bearer secret, fail-closed when the secret is unset.
//
// It notifies the owner of every scheduled promotion whose send time
// has arrived, so the assisted WhatsApp queue is ready when they open
// the app.
// POST /api/v1/internal/jobs/promotions-push
func PromotionsPushJob(db *gorm.DB, dispatcher *push.Dispatcher) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}

		result, err := jobs.RunPromotionsPush(db, time.Now().UTC(), dispatcher)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo ejecutar el job de promociones",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"notified": result.Notified})
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, returning "" when the header is missing or malformed.
func bearerToken(header string) string {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
