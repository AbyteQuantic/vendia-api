// Spec: specs/016-ia-foto-async-polling/spec.md
package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAIJobDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AIJob{}))
	return db
}

// T-01 — RED: a freshly created AIJob is born `processing` with a
// generated UUID and a non-zero CreatedAt; the constants pin the
// enum values the handlers and the frontend agree on.
func TestAIJob_CreatedAsProcessing(t *testing.T) {
	db := setupAIJobDB(t)

	job := AIJob{
		TenantID:  "11111111-1111-1111-1111-111111111111",
		ProductID: "22222222-2222-2222-2222-222222222222",
		JobType:   AIJobTypeEnhance,
		Status:    AIJobStatusProcessing,
	}
	require.NoError(t, db.Create(&job).Error)

	assert.NotEmpty(t, job.ID, "BeforeCreate must generate a UUID")
	assert.True(t, IsValidUUID(job.ID), "generated ID must be a valid UUID v4")
	assert.False(t, job.CreatedAt.IsZero(), "CreatedAt must be set on create")
	assert.Equal(t, AIJobStatusProcessing, job.Status)
	assert.Equal(t, AIJobTypeEnhance, job.JobType)
	assert.Empty(t, job.ResultPhotoURL, "a processing job has no result yet")
	assert.Empty(t, job.ErrorMessage, "a processing job has no error yet")
}

// T-01 — RED: the happy path of the job lifecycle. A `processing` job
// transitions to `done` carrying the resulting photo URL.
func TestAIJob_TransitionsToDone(t *testing.T) {
	db := setupAIJobDB(t)

	job := AIJob{
		TenantID:  "11111111-1111-1111-1111-111111111111",
		ProductID: "22222222-2222-2222-2222-222222222222",
		JobType:   AIJobTypeGenerate,
		Status:    AIJobStatusProcessing,
	}
	require.NoError(t, db.Create(&job).Error)

	// Background goroutine finished successfully.
	require.NoError(t, db.Model(&job).Updates(map[string]any{
		"status":           AIJobStatusDone,
		"result_photo_url": "https://cdn.vendia.store/products/x-enhanced.png",
	}).Error)

	var reloaded AIJob
	require.NoError(t, db.First(&reloaded, "id = ?", job.ID).Error)
	assert.Equal(t, AIJobStatusDone, reloaded.Status)
	assert.Equal(t, "https://cdn.vendia.store/products/x-enhanced.png", reloaded.ResultPhotoURL)
	assert.Empty(t, reloaded.ErrorMessage, "a done job carries no error")
}

// T-01 — RED: the failure path. A `processing` job transitions to
// `failed` carrying a user-facing Spanish reason (Art. V).
func TestAIJob_TransitionsToFailed(t *testing.T) {
	db := setupAIJobDB(t)

	job := AIJob{
		TenantID:  "11111111-1111-1111-1111-111111111111",
		ProductID: "22222222-2222-2222-2222-222222222222",
		JobType:   AIJobTypeEnhance,
		Status:    AIJobStatusProcessing,
	}
	require.NoError(t, db.Create(&job).Error)

	const reason = "No pudimos mejorar la foto. Intenta de nuevo."
	require.NoError(t, db.Model(&job).Updates(map[string]any{
		"status":        AIJobStatusFailed,
		"error_message": reason,
	}).Error)

	var reloaded AIJob
	require.NoError(t, db.First(&reloaded, "id = ?", job.ID).Error)
	assert.Equal(t, AIJobStatusFailed, reloaded.Status)
	assert.Equal(t, reason, reloaded.ErrorMessage)
	assert.Empty(t, reloaded.ResultPhotoURL, "a failed job carries no result URL")
}

// T-01 — RED: ai_jobs is the table name the AutoMigrate registration
// and every query rely on.
func TestAIJob_TableName(t *testing.T) {
	assert.Equal(t, "ai_jobs", AIJob{}.TableName())
}

// T-01 — RED: CreatedAt is not overwritten when the caller supplies
// one. GetAIJob's "stale processing job" rule (D4) depends on the
// stored CreatedAt being trustworthy.
func TestAIJob_KeepsSuppliedCreatedAt(t *testing.T) {
	db := setupAIJobDB(t)

	fixed := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	job := AIJob{
		BaseModel: BaseModel{CreatedAt: fixed},
		TenantID:  "11111111-1111-1111-1111-111111111111",
		ProductID: "22222222-2222-2222-2222-222222222222",
		JobType:   AIJobTypeEnhance,
		Status:    AIJobStatusProcessing,
	}
	require.NoError(t, db.Create(&job).Error)
	assert.Equal(t, fixed.Unix(), job.CreatedAt.Unix(),
		"a caller-supplied CreatedAt must survive BeforeCreate")
}
