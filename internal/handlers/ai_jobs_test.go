// Spec: specs/016-ia-foto-async-polling/spec.md
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupAIJobsDB opens an in-memory sqlite DB with the minimal schema
// the async AI-photo flow touches: products (the entity whose photo is
// updated) and ai_jobs (the polling record).
func setupAIJobsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.AIJob{}))
	return db
}

func mountGetAIJob(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.GET("/products/:id/ai-job/:jobId", GetAIJob(db))
	return r
}

func getAIJobJSON(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ── T-04 — runAIJob: the background job lifecycle ───────────────────

// T-04 — RED: a job whose worker succeeds is flipped to `done` and
// carries the URL the worker returned.
func TestRunAIJob_SuccessMarksDone(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "11111111-1111-1111-1111-111111111111"
	productID := "22222222-2222-2222-2222-222222222222"

	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeEnhance)
	require.NoError(t, err)
	require.Equal(t, models.AIJobStatusProcessing, job.Status)

	worker := func(ctx context.Context) (string, error) {
		return "https://cdn.vendia.store/products/done.png", nil
	}
	runAIJob(db, job.ID, productID, tenantID, worker)

	var reloaded models.AIJob
	require.NoError(t, db.First(&reloaded, "id = ?", job.ID).Error)
	assert.Equal(t, models.AIJobStatusDone, reloaded.Status)
	assert.Equal(t, "https://cdn.vendia.store/products/done.png", reloaded.ResultPhotoURL)
	assert.Empty(t, reloaded.ErrorMessage)
}

// T-04 — RED: a job whose worker returns a plain error is flipped to
// `failed` with the generic Spanish message — the raw error never
// reaches the row the client polls (Art. V).
func TestRunAIJob_FailureMarksFailed(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "11111111-1111-1111-1111-111111111111"
	productID := "22222222-2222-2222-2222-222222222222"

	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeGenerate)
	require.NoError(t, err)

	worker := func(ctx context.Context) (string, error) {
		return "", errors.New("gemini upstream 500")
	}
	runAIJob(db, job.ID, productID, tenantID, worker)

	var reloaded models.AIJob
	require.NoError(t, db.First(&reloaded, "id = ?", job.ID).Error)
	assert.Equal(t, models.AIJobStatusFailed, reloaded.Status)
	assert.Equal(t, aiJobGenericFailMessage, reloaded.ErrorMessage)
	assert.NotContains(t, reloaded.ErrorMessage, "gemini", "raw error must not leak — Art. V")
	assert.Empty(t, reloaded.ResultPhotoURL)
}

// T-04 — RED: a worker that returns a context-deadline error maps to
// the clean Spanish timeout message, not the generic one.
func TestRunAIJob_TimeoutMapsToTimeoutMessage(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "11111111-1111-1111-1111-111111111111"
	productID := "22222222-2222-2222-2222-222222222222"

	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeEnhance)
	require.NoError(t, err)

	worker := func(ctx context.Context) (string, error) {
		return "", context.DeadlineExceeded
	}
	runAIJob(db, job.ID, productID, tenantID, worker)

	var reloaded models.AIJob
	require.NoError(t, db.First(&reloaded, "id = ?", job.ID).Error)
	assert.Equal(t, models.AIJobStatusFailed, reloaded.Status)
	assert.Equal(t, aiTimeoutMessage, reloaded.ErrorMessage)
}

// T-04 — RED: a panic inside the worker must not crash the process —
// runAIJob recovers it and records a failed job.
func TestRunAIJob_RecoversWorkerPanic(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "11111111-1111-1111-1111-111111111111"
	productID := "22222222-2222-2222-2222-222222222222"

	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeEnhance)
	require.NoError(t, err)

	worker := func(ctx context.Context) (string, error) {
		panic("boom in worker")
	}
	assert.NotPanics(t, func() {
		runAIJob(db, job.ID, productID, tenantID, worker)
	})

	var reloaded models.AIJob
	require.NoError(t, db.First(&reloaded, "id = ?", job.ID).Error)
	assert.Equal(t, models.AIJobStatusFailed, reloaded.Status)
	assert.Equal(t, aiJobGenericFailMessage, reloaded.ErrorMessage)
}

// T-04 — D2: the background goroutine must get its OWN context, not
// the request context. runAIJob builds it from context.Background()
// with a 120s timeout — the worker therefore receives a non-cancelled
// context with a deadline.
func TestRunAIJob_GivesWorkerOwnLiveContext(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "t"
	productID := "p"
	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeEnhance)
	require.NoError(t, err)

	var gotErr error
	var hadDeadline bool
	worker := func(ctx context.Context) (string, error) {
		gotErr = ctx.Err()
		_, hadDeadline = ctx.Deadline()
		return "ok", nil
	}
	runAIJob(db, job.ID, productID, tenantID, worker)

	assert.NoError(t, gotErr, "the background context must be live, not cancelled")
	assert.True(t, hadDeadline, "the background context must carry a deadline (~120s)")
}

func TestAIJobBackgroundTimeout_IsGenerous(t *testing.T) {
	assert.Equal(t, 120*time.Second, aiJobBackgroundTimeout,
		"the background AI job budget must be 120s — Spec D2")
}

// ── T-05 — GetAIJob: the polling endpoint ───────────────────────────

// T-05 — RED: a `processing` job is reported as processing, with no
// photo_url and no error in the envelope.
func TestGetAIJob_Processing(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "tenant-a"
	productID := "prod-a"
	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeEnhance)
	require.NoError(t, err)

	r := mountGetAIJob(db, tenantID)
	w := getAIJobJSON(t, r, "/products/"+productID+"/ai-job/"+job.ID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Status   string `json:"status"`
			PhotoURL string `json:"photo_url"`
			Error    string `json:"error"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.AIJobStatusProcessing, resp.Data.Status)
	assert.Empty(t, resp.Data.PhotoURL)
	assert.Empty(t, resp.Data.Error)
}

// T-05 — RED: a `done` job is reported with the resulting photo_url.
func TestGetAIJob_Done(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "tenant-a"
	productID := "prod-a"
	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeGenerate)
	require.NoError(t, err)
	require.NoError(t, db.Model(&job).Updates(map[string]any{
		"status":           models.AIJobStatusDone,
		"result_photo_url": "https://cdn.vendia.store/p/new.png",
	}).Error)

	r := mountGetAIJob(db, tenantID)
	w := getAIJobJSON(t, r, "/products/"+productID+"/ai-job/"+job.ID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Status   string `json:"status"`
			PhotoURL string `json:"photo_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.AIJobStatusDone, resp.Data.Status)
	assert.Equal(t, "https://cdn.vendia.store/p/new.png", resp.Data.PhotoURL)
}

// T-05 — RED: a `failed` job is reported with its Spanish error.
func TestGetAIJob_Failed(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "tenant-a"
	productID := "prod-a"
	job, err := createAIJob(db, tenantID, productID, models.AIJobTypeEnhance)
	require.NoError(t, err)
	require.NoError(t, db.Model(&job).Updates(map[string]any{
		"status":        models.AIJobStatusFailed,
		"error_message": "No pudimos mejorar la foto. Intenta de nuevo.",
	}).Error)

	r := mountGetAIJob(db, tenantID)
	w := getAIJobJSON(t, r, "/products/"+productID+"/ai-job/"+job.ID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.AIJobStatusFailed, resp.Data.Status)
	assert.Equal(t, "No pudimos mejorar la foto. Intenta de nuevo.", resp.Data.Error)
}

// T-05 — RED / FR-06 / D4: a job stuck `processing` longer than the
// stale threshold is reported as `failed` with the "tardó demasiado"
// message, AND the row is persisted as failed so the next poll is
// consistent.
func TestGetAIJob_StaleProcessingBecomesFailed(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "tenant-a"
	productID := "prod-a"

	// A job created 6 minutes ago that never finished (backend restart).
	staleID := "99999999-9999-4999-8999-999999999999"
	require.NoError(t, db.Create(&models.AIJob{
		BaseModel: models.BaseModel{
			ID:        staleID,
			CreatedAt: time.Now().Add(-6 * time.Minute),
		},
		TenantID:  tenantID,
		ProductID: productID,
		JobType:   models.AIJobTypeEnhance,
		Status:    models.AIJobStatusProcessing,
	}).Error)

	r := mountGetAIJob(db, tenantID)
	w := getAIJobJSON(t, r, "/products/"+productID+"/ai-job/"+staleID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.AIJobStatusFailed, resp.Data.Status,
		"a processing job older than 5 min must be reported failed — FR-06")
	assert.Equal(t, aiJobStaleMessage, resp.Data.Error)

	// The transition was persisted — the row itself is now failed.
	var reloaded models.AIJob
	require.NoError(t, db.First(&reloaded, "id = ?", staleID).Error)
	assert.Equal(t, models.AIJobStatusFailed, reloaded.Status,
		"the stale transition must be persisted, not just reported")
	assert.Equal(t, aiJobStaleMessage, reloaded.ErrorMessage)
}

// T-05 — a recent `processing` job (under the threshold) stays
// processing — the stale rule must not fire early.
func TestGetAIJob_RecentProcessingStaysProcessing(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "tenant-a"
	productID := "prod-a"

	recentID := "88888888-8888-4888-8888-888888888888"
	require.NoError(t, db.Create(&models.AIJob{
		BaseModel: models.BaseModel{
			ID:        recentID,
			CreatedAt: time.Now().Add(-30 * time.Second),
		},
		TenantID:  tenantID,
		ProductID: productID,
		JobType:   models.AIJobTypeEnhance,
		Status:    models.AIJobStatusProcessing,
	}).Error)

	r := mountGetAIJob(db, tenantID)
	w := getAIJobJSON(t, r, "/products/"+productID+"/ai-job/"+recentID)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.AIJobStatusProcessing, resp.Data.Status)
}

// T-05 / FR-07 / Art. III — a job belonging to another tenant is
// invisible: GetAIJob returns 404, never another tenant's job.
func TestGetAIJob_TenantIsolation(t *testing.T) {
	db := setupAIJobsDB(t)
	productID := "prod-shared"
	// Job owned by tenant-b.
	job, err := createAIJob(db, "tenant-b", productID, models.AIJobTypeEnhance)
	require.NoError(t, err)

	// tenant-a polls tenant-b's job → 404.
	r := mountGetAIJob(db, "tenant-a")
	w := getAIJobJSON(t, r, "/products/"+productID+"/ai-job/"+job.ID)
	assert.Equal(t, http.StatusNotFound, w.Code,
		"a tenant must never read another tenant's AI job — Art. III")
}

// T-05 — the job must belong to the product in the URL: a mismatched
// product_id is a 404, not a cross-product leak.
func TestGetAIJob_ProductMismatchReturns404(t *testing.T) {
	db := setupAIJobsDB(t)
	tenantID := "tenant-a"
	job, err := createAIJob(db, tenantID, "prod-real", models.AIJobTypeEnhance)
	require.NoError(t, err)

	r := mountGetAIJob(db, tenantID)
	w := getAIJobJSON(t, r, "/products/prod-other/ai-job/"+job.ID)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// T-05 — an unknown job id is a clean 404.
func TestGetAIJob_UnknownJobReturns404(t *testing.T) {
	db := setupAIJobsDB(t)
	r := mountGetAIJob(db, "tenant-a")
	w := getAIJobJSON(t, r, "/products/prod-a/ai-job/00000000-0000-4000-8000-000000000000")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAIJobStaleMessages_AreSpanish(t *testing.T) {
	for _, msg := range []string{aiJobStaleMessage, aiJobGenericFailMessage} {
		assert.NotEmpty(t, msg)
		assert.NotContains(t, msg, "context")
		assert.NotContains(t, msg, "deadline")
		assert.NotContains(t, msg, "error:")
	}
	assert.Contains(t, aiJobStaleMessage, "IA")
}
