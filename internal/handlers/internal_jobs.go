// Spec: specs/031-cotizaciones/spec.md
package handlers

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"vendia-backend/internal/jobs"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

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
		expected := strings.TrimSpace(os.Getenv("CRON_TOKEN"))
		if expected == "" {
			// Fail closed — never run the job without a configured secret.
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "endpoint de cron no configurado",
			})
			return
		}

		provided := bearerToken(c.GetHeader("Authorization"))
		// Constant-time compare to avoid leaking the secret via timing.
		if provided == "" ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token de cron inválido"})
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

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, returning "" when the header is missing or malformed.
func bearerToken(header string) string {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
