// Spec: specs/016-ia-foto-async-polling/spec.md
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// aiJobBackgroundTimeout is the budget given to the background
// goroutine that runs the real AI photo work (download + Gemini +
// upload). It uses its OWN context derived from context.Background()
// — NOT the request context, which is cancelled the instant the
// handler answers the 202. Spec D2: ~120s.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — §3, D2.
const aiJobBackgroundTimeout = 120 * time.Second

// aiJobStaleAfter is how long a job may stay `processing` before
// GetAIJob considers it dead. If the backend restarted mid-job (Render
// free is a single instance) the goroutine is gone and the row would
// otherwise hang `processing` forever — the client would poll until
// its own ~3 min cap. Spec D4 / FR-06: a `processing` job older than
// this is reported as `failed`.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — §3, D4, FR-06.
const aiJobStaleAfter = 5 * time.Minute

// aiJobStaleMessage is the Spanish, user-facing reason returned when a
// job is found stuck `processing` past aiJobStaleAfter. Constitution
// Art. V — the shopkeeper never sees a raw technical reason.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — FR-06 / Art. V.
const aiJobStaleMessage = "La IA está tardando demasiado. Intenta de nuevo."

// aiJobGenericFailMessage is the Spanish fallback shown when the
// background worker returns an error that is not a timeout — the raw
// Go error never reaches the shopkeeper (Art. V).
const aiJobGenericFailMessage = "No pudimos procesar la foto con IA. Intenta de nuevo."

// aiPhotoWorker is the real, slow AI photo work for one job: download
// the source photo, call Gemini, upload the result, and return the new
// photo URL. It is a function type so the handlers can inject the
// production implementation and the tests can inject a fast double —
// no live Gemini/R2 needed to verify the async job machinery.
//
// The ctx passed in is the background context (see runAIJob); the
// worker must respect it for cancellation/timeout.
type aiPhotoWorker func(ctx context.Context) (photoURL string, err error)

// launchAIJob is the seam that fires the background job. In production
// it spawns runAIJob in its own goroutine — fully decoupled from the
// request, which has already returned its 202. Tests swap this for a
// synchronous double so they exercise the handler's 202 contract
// without a live Gemini/R2 call or a leaked goroutine.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — §3, D2.
var launchAIJob = func(db *gorm.DB, jobID, productID, tenantID string, worker aiPhotoWorker) {
	go runAIJob(db, jobID, productID, tenantID, worker)
}

// runAIJob executes one AI photo job in the background. It is launched
// in a goroutine by the enhance/generate handlers AFTER they have
// already answered the client with 202. It owns the job's whole
// lifecycle past `processing`:
//
//  1. Build a fresh context from context.Background() + timeout — the
//     request context is dead by now, so it MUST NOT be used (Spec D2).
//  2. Run the injected worker (download + Gemini + upload).
//  3. On success → flip the AIJob row to `done` with the result URL,
//     and point the product's photo at the new URL (same UPDATE the
//     old synchronous handler did).
//  4. On failure → flip the AIJob row to `failed` with a Spanish
//     reason; a timeout maps to the same clean message the F015 sync
//     path used.
//
// runAIJob never panics the process: a panic inside the worker is
// recovered and recorded as a failed job.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — §3, FR-01, FR-02.
func runAIJob(db *gorm.DB, jobID, productID, tenantID string, worker aiPhotoWorker) {
	ctx, cancel := context.WithTimeout(context.Background(), aiJobBackgroundTimeout)
	defer cancel()
	// El worker corre con context.Background() (sin el ctx del request). Inyectamos el
	// tenant para que recordTokenUsage SÍ registre el uso de IA (model_name) de los jobs
	// de foto — antes se perdía (tenant vacío → no se logueaba el modelo usado).
	ctx = aiusage.WithTenantID(ctx, tenantID)

	photoURL, err := func() (url string, runErr error) {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("ai job panic: %v", r)
			}
		}()
		return worker(ctx)
	}()

	if err != nil {
		var msg, category string
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil:
			msg = aiTimeoutMessage
			category = "timeout"
		default:
			msg = classifyAIJobError(err)
			category = aiJobErrorCategory(msg)
		}
		// Logging estructurado para grep en Render: la categoría va antes
		// del error crudo, así un grep "[ai-job-fail category=" agrupa
		// fallas por tipo sin tener que parsear el error completo.
		log.Printf("[ai-job-fail category=%s id=%s] %v", category, jobID, err)
		if uerr := db.Model(&models.AIJob{}).
			Where("id = ?", jobID).
			Updates(map[string]any{
				"status":        models.AIJobStatusFailed,
				"error_message": msg,
			}).Error; uerr != nil {
			log.Printf("[ai-job %s] could not persist failed status: %v", jobID, uerr)
		}
		return
	}

	if uerr := db.Model(&models.AIJob{}).
		Where("id = ?", jobID).
		Updates(map[string]any{
			"status":           models.AIJobStatusDone,
			"result_photo_url": photoURL,
		}).Error; uerr != nil {
		log.Printf("[ai-job %s] could not persist done status: %v", jobID, uerr)
	}
}

// createAIJob inserts a fresh `processing` AIJob row for a product and
// returns it. It is the first thing the enhance/generate handlers do
// before launching the background goroutine.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — FR-01.
func createAIJob(db *gorm.DB, tenantID, productID, jobType string) (models.AIJob, error) {
	job := models.AIJob{
		TenantID:  tenantID,
		ProductID: productID,
		JobType:   jobType,
		Status:    models.AIJobStatusProcessing,
	}
	if err := db.Create(&job).Error; err != nil {
		return models.AIJob{}, fmt.Errorf("could not create ai job: %w", err)
	}
	return job, nil
}

// respondAIJobAccepted writes the standard 202 envelope the
// enhance/generate handlers return the moment the job is queued. The
// client takes the job_id and starts polling GetAIJob.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — §4 (contracts).
func respondAIJobAccepted(c *gin.Context, jobID string) {
	c.JSON(http.StatusAccepted, gin.H{"data": gin.H{
		"job_id": jobID,
		"status": models.AIJobStatusProcessing,
	}})
}

// GetAIJob is the polling endpoint: GET /products/:id/ai-job/:jobId.
// It returns the current state of an AI photo job, scoped to the
// caller's tenant (Art. III) — a tenant can never read another
// tenant's job.
//
// The response shape is {data:{status, photo_url?, error?}}:
//   - processing → still running; the client keeps polling.
//   - done       → photo_url carries the new product photo.
//   - failed     → error carries a clear Spanish reason.
//
// Stale-job rule (Spec D4 / FR-06): if the job is still `processing`
// but its created_at is older than aiJobStaleAfter, the backend
// restarted mid-job and the goroutine is gone. GetAIJob reports it as
// `failed` with a clear message AND persists that transition so every
// later poll is consistent — the client never waits forever.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — FR-02, FR-06, FR-07.
func GetAIJob(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productID := c.Param("id")
		jobID := c.Param("jobId")

		var job models.AIJob
		// Scope by tenant_id AND product_id — Art. III isolation plus a
		// guard that the job actually belongs to the product in the URL.
		if err := db.Where("id = ? AND tenant_id = ? AND product_id = ?",
			jobID, tenantID, productID).First(&job).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "trabajo de IA no encontrado"})
			return
		}

		status := job.Status
		photoURL := job.ResultPhotoURL
		errorMsg := job.ErrorMessage

		// Stale-job self-heal: a job stuck `processing` past the
		// threshold is treated — and persisted — as `failed`.
		if status == models.AIJobStatusProcessing &&
			time.Since(job.CreatedAt) > aiJobStaleAfter {
			status = models.AIJobStatusFailed
			errorMsg = aiJobStaleMessage
			db.Model(&models.AIJob{}).
				Where("id = ? AND status = ?", job.ID, models.AIJobStatusProcessing).
				Updates(map[string]any{
					"status":        models.AIJobStatusFailed,
					"error_message": aiJobStaleMessage,
				})
		}

		resp := gin.H{"status": status}
		if status == models.AIJobStatusDone {
			resp["photo_url"] = photoURL
		}
		if status == models.AIJobStatusFailed {
			resp["error"] = errorMsg
		}
		c.JSON(http.StatusOK, gin.H{"data": resp})
	}
}
