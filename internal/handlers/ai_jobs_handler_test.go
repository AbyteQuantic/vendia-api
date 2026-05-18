// Spec: specs/016-ia-foto-async-polling/spec.md
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// withStubbedLaunch swaps the launchAIJob seam for the duration of one
// test. The stub records the worker and runs the job SYNCHRONOUSLY in
// the calling goroutine — so the test stays deterministic, never
// touches the network, and leaks no background goroutine (-race safe).
// The original launcher is restored on cleanup. Tests using it run
// serially (no t.Parallel) because launchAIJob is package-global.
func withStubbedLaunch(t *testing.T) *launchRecorder {
	t.Helper()
	rec := &launchRecorder{}
	original := launchAIJob
	launchAIJob = func(db *gorm.DB, jobID, productID, tenantID string, worker aiPhotoWorker) {
		rec.mu.Lock()
		rec.calls++
		rec.lastJobID = jobID
		rec.lastProductID = productID
		rec.lastTenantID = tenantID
		rec.mu.Unlock()
		// Run synchronously so the ai_jobs row is settled by the time
		// the handler returns — no goroutine, no race.
		runAIJob(db, jobID, productID, tenantID, worker)
	}
	t.Cleanup(func() { launchAIJob = original })
	return rec
}

type launchRecorder struct {
	mu            sync.Mutex
	calls         int
	lastJobID     string
	lastProductID string
	lastTenantID  string
}

// mountAIPhoto wires the async AI-photo endpoints with the supplied
// (possibly fake) Gemini + storage services and a fixed tenant claim,
// so a test can drive the full 202 path.
func mountAIPhoto(db *gorm.DB, tenantID string, gemini *services.GeminiService, storage services.FileStorage) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.POST("/products/:id/enhance", EnhanceProductPhoto(db, gemini, storage, nil))
	r.POST("/products/:id/generate-image", GenerateProductImage(db, gemini, storage, nil))
	return r
}

func postNoBody(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// T-03 — RED: EnhanceProductPhoto answers 202 immediately with a
// job_id + status:"processing", and creates the matching ai_jobs row.
// The background work is stubbed to fail fast — the 202 contract is
// what this test pins, independent of Gemini's outcome.
func TestEnhanceProductPhoto_Responds202AndCreatesJob(t *testing.T) {
	rec := withStubbedLaunch(t)
	db := setupAIJobsDB(t)
	tenantID := "tenant-enh"
	productID := "11111111-1111-4111-8111-111111111111"

	// A product WITH a photo — enhance needs a source image.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500,
		PhotoURL: "https://example.invalid/old.png",
	}).Error)

	r := mountAIPhoto(db, tenantID, &services.GeminiService{}, newFakeStorage())
	w := postNoBody(t, r, "/products/"+productID+"/enhance")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			JobID  string `json:"job_id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.JobID, "the 202 must carry a job_id")
	assert.Equal(t, models.AIJobStatusProcessing, resp.Data.Status)

	// The ai_jobs row exists, is scoped to the tenant + product.
	var job models.AIJob
	require.NoError(t, db.First(&job, "id = ?", resp.Data.JobID).Error)
	assert.Equal(t, tenantID, job.TenantID)
	assert.Equal(t, productID, job.ProductID)
	assert.Equal(t, models.AIJobTypeEnhance, job.JobType)

	// The handler fired the background launcher exactly once with the
	// correct identifiers.
	assert.Equal(t, 1, rec.calls)
	assert.Equal(t, resp.Data.JobID, rec.lastJobID)
	assert.Equal(t, productID, rec.lastProductID)
	assert.Equal(t, tenantID, rec.lastTenantID)
}

// T-03 — RED: GenerateProductImage answers 202 + creates a `generate`
// job. Generate does not need an existing photo.
func TestGenerateProductImage_Responds202AndCreatesJob(t *testing.T) {
	rec := withStubbedLaunch(t)
	db := setupAIJobsDB(t)
	tenantID := "tenant-gen"
	productID := "22222222-2222-4222-8222-222222222222"

	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Arroz", Price: 3000,
	}).Error)

	r := mountAIPhoto(db, tenantID, &services.GeminiService{}, newFakeStorage())
	w := postNoBody(t, r, "/products/"+productID+"/generate-image")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			JobID  string `json:"job_id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.JobID)
	assert.Equal(t, models.AIJobStatusProcessing, resp.Data.Status)

	var job models.AIJob
	require.NoError(t, db.First(&job, "id = ?", resp.Data.JobID).Error)
	assert.Equal(t, models.AIJobTypeGenerate, job.JobType)
	assert.Equal(t, tenantID, job.TenantID)
	assert.Equal(t, 1, rec.calls)
}

// T-03 — end-to-end through the handler with a fully successful
// worker: the 202 fires, then the (stubbed-synchronous) job flips the
// row to `done` and points the product photo at the new URL.
func TestEnhanceProductPhoto_JobReachesDoneAndUpdatesProduct(t *testing.T) {
	t.Helper()
	rec := &launchRecorder{}
	original := launchAIJob
	const newURL = "https://cdn.vendia.store/products/enhanced-ok.png"
	launchAIJob = func(db *gorm.DB, jobID, productID, tenantID string, _ aiPhotoWorker) {
		rec.calls++
		// Substitute a worker that succeeds without any network.
		runAIJob(db, jobID, productID, tenantID, func(context.Context) (string, error) {
			db.Model(&models.Product{}).
				Where("id = ?", productID).
				Update("photo_url", newURL)
			return newURL, nil
		})
	}
	t.Cleanup(func() { launchAIJob = original })

	db := setupAIJobsDB(t)
	tenantID := "tenant-enh"
	productID := "66666666-6666-4666-8666-666666666666"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500,
		PhotoURL: "https://example.invalid/old.png",
	}).Error)

	r := mountAIPhoto(db, tenantID, &services.GeminiService{}, newFakeStorage())
	w := postNoBody(t, r, "/products/"+productID+"/enhance")
	require.Equal(t, http.StatusAccepted, w.Code)

	var resp struct {
		Data struct {
			JobID string `json:"job_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	var job models.AIJob
	require.NoError(t, db.First(&job, "id = ?", resp.Data.JobID).Error)
	assert.Equal(t, models.AIJobStatusDone, job.Status)
	assert.Equal(t, newURL, job.ResultPhotoURL)

	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", productID).Error)
	assert.Equal(t, newURL, product.PhotoURL, "the product photo must be updated on done")
}

// T-03 — a failing worker leaves the job `failed` with a Spanish
// reason, reached through the handler.
func TestGenerateProductImage_JobReachesFailed(t *testing.T) {
	rec := &launchRecorder{}
	original := launchAIJob
	launchAIJob = func(db *gorm.DB, jobID, productID, tenantID string, _ aiPhotoWorker) {
		rec.calls++
		runAIJob(db, jobID, productID, tenantID, func(context.Context) (string, error) {
			return "", errors.New("gemini quota exceeded")
		})
	}
	t.Cleanup(func() { launchAIJob = original })

	db := setupAIJobsDB(t)
	tenantID := "tenant-gen"
	productID := "77777777-7777-4777-8777-777777777777"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Arroz", Price: 3000,
	}).Error)

	r := mountAIPhoto(db, tenantID, &services.GeminiService{}, newFakeStorage())
	w := postNoBody(t, r, "/products/"+productID+"/generate-image")
	require.Equal(t, http.StatusAccepted, w.Code)

	var resp struct {
		Data struct {
			JobID string `json:"job_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	var job models.AIJob
	require.NoError(t, db.First(&job, "id = ?", resp.Data.JobID).Error)
	assert.Equal(t, models.AIJobStatusFailed, job.Status)
	assert.Equal(t, aiJobGenericFailMessage, job.ErrorMessage)
	assert.NotContains(t, job.ErrorMessage, "gemini", "raw error must not leak — Art. V")
}

// T-03 — a product with no photo cannot be enhanced — 400, and NO job
// row is created (the work never starts).
func TestEnhanceProductPhoto_NoPhotoRejectedNoJob(t *testing.T) {
	withStubbedLaunch(t)
	db := setupAIJobsDB(t)
	tenantID := "tenant-enh"
	productID := "33333333-3333-4333-8333-333333333333"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Sin foto", Price: 1000,
	}).Error)

	r := mountAIPhoto(db, tenantID, &services.GeminiService{}, newFakeStorage())
	w := postNoBody(t, r, "/products/"+productID+"/enhance")
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var count int64
	require.NoError(t, db.Model(&models.AIJob{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "a rejected request must not leave a job behind")
}

// T-03 — a missing AI service yields 503, no job.
func TestEnhanceProductPhoto_ServiceUnavailableNoJob(t *testing.T) {
	withStubbedLaunch(t)
	db := setupAIJobsDB(t)
	tenantID := "tenant-enh"
	productID := "44444444-4444-4444-8444-444444444444"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500,
		PhotoURL: "https://example.invalid/old.png",
	}).Error)

	// nil Gemini service → not configured.
	r := mountAIPhoto(db, tenantID, nil, newFakeStorage())
	w := postNoBody(t, r, "/products/"+productID+"/enhance")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	var count int64
	require.NoError(t, db.Model(&models.AIJob{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

// T-03 — Art. III: enhancing another tenant's product is a 404.
func TestEnhanceProductPhoto_ForeignProductReturns404(t *testing.T) {
	withStubbedLaunch(t)
	db := setupAIJobsDB(t)
	productID := "55555555-5555-4555-8555-555555555555"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  "tenant-owner", Name: "Ajeno", Price: 1000,
		PhotoURL: "https://example.invalid/x.png",
	}).Error)

	r := mountAIPhoto(db, "tenant-attacker", &services.GeminiService{}, newFakeStorage())
	w := postNoBody(t, r, "/products/"+productID+"/enhance")
	assert.Equal(t, http.StatusNotFound, w.Code)
}
