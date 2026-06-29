// Spec: specs/031-cotizaciones/spec.md
package handlers

import (
	"crypto/subtle"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"vendia-backend/internal/jobs"
	"vendia-backend/internal/services/email"
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

// AgendaRemindersJob — aviso diario a cada salón con sus turnos de hoy (Spec
// 084 backlog #1). Mismo modelo de auth que los demás jobs (CRON_TOKEN Bearer,
// fail-closed). POST /api/v1/internal/jobs/agenda-reminders
func AgendaRemindersJob(db *gorm.DB, dispatcher *push.Dispatcher) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}
		result, err := jobs.RunAgendaReminders(db, time.Now().UTC(), dispatcher)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo ejecutar el job de agenda",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"notified": result.Notified})
	}
}

// EventRemindersJob is the internal cron endpoint that emails attendees about
// upcoming events and pending installments, and pushes each organizer a
// summary (Spec F042 FR-20). Same auth model as the other internal jobs:
// no JWT, gated by the shared CRON_TOKEN secret, fail-closed when unset.
// POST /api/v1/internal/jobs/event-reminders
func EventRemindersJob(db *gorm.DB, dispatcher *push.Dispatcher, emailSvc *email.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}
		result, err := jobs.RunEventReminders(db, time.Now().UTC(), dispatcher, emailSvc)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo ejecutar el job de recordatorios de eventos",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}

// CapacityCheckJob — POST /api/v1/internal/jobs/capacity-check (Spec 093/091).
// Monitoreo de capacidad: mide el tamaño de la DB (Supabase free ~500MB) y avisa
// al rozar el umbral (70% = 350MB) para disparar la retención (Spec 091) ANTES de
// llegar al límite. Mismo gate CRON_TOKEN, fail-closed. Devuelve `warn:true` y
// HTTP 507 (Insufficient Storage) cuando se pasa el umbral → el cron falla y
// GitHub envía correo al dueño del workflow ("avísame"). Las tablas top ayudan a
// decidir qué archivar.
func CapacityCheckJob(db *gorm.DB) gin.HandlerFunc {
	const freeMB = 500
	const warnMB = 350 // 70% de 500
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}
		var bytes int64
		if err := db.Raw("SELECT pg_database_size(current_database())").
			Scan(&bytes).Error; err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo medir el tamaño de la DB"})
			return
		}
		type tbl struct {
			Table string `json:"table"`
			Bytes int64  `json:"bytes"`
		}
		var tables []tbl
		db.Raw(`SELECT relname AS table, pg_total_relation_size(relid) AS bytes
		        FROM pg_catalog.pg_statio_user_tables
		        ORDER BY pg_total_relation_size(relid) DESC LIMIT 8`).Scan(&tables)

		mb := float64(bytes) / (1024 * 1024)
		pct := mb / freeMB * 100
		warn := mb >= warnMB
		body := gin.H{
			"db_mb":        math.Round(mb*10) / 10,
			"pct_of_free":  math.Round(pct*10) / 10,
			"threshold_mb": warnMB,
			"free_mb":      freeMB,
			"warn":         warn,
			"top_tables":   tables,
		}
		if warn {
			// 507 → el cron falla → notificación de GitHub. Spec 091 a ejecutar.
			c.JSON(http.StatusInsufficientStorage, body)
			return
		}
		c.JSON(http.StatusOK, body)
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
