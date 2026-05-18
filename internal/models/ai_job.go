// Spec: specs/016-ia-foto-async-polling/spec.md
package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AIJob — Job types. An AI photo job is either an "enhance" of an
// existing product photo or a "generate" of a brand-new one.
const (
	AIJobTypeEnhance  = "enhance"
	AIJobTypeGenerate = "generate"
)

// AIJob — Job statuses. A job is born "processing", and the background
// goroutine flips it to "done" (with ResultPhotoURL) or "failed" (with
// ErrorMessage). There is no terminal state in between.
const (
	AIJobStatusProcessing = "processing"
	AIJobStatusDone       = "done"
	AIJobStatusFailed     = "failed"
)

// AIJob is the persistent record of one asynchronous AI photo
// operation (enhance or generate). The handler creates it as
// `processing` and responds 202 immediately; a background goroutine
// runs the real work (download + Gemini + upload) with its own
// context and flips the row to `done`/`failed` when it finishes.
//
// Persisting the job in Postgres — instead of in memory — is decision
// D1 of the spec: Render free is a single instance that may restart,
// and a persistent row both survives the restart and lets GetAIJob
// report a clear `failed` reason.
//
// Spec: specs/016-ia-foto-async-polling/spec.md — §3, D1, FR-07.
type AIJob struct {
	BaseModel
	// TenantID isolates the job to its owner — Constitution Art. III.
	// Every GetAIJob query filters by this column.
	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	// ProductID is the product whose photo this job enhances/generates.
	ProductID string `gorm:"type:uuid;not null;index" json:"product_id"`
	// JobType is one of AIJobTypeEnhance | AIJobTypeGenerate.
	JobType string `gorm:"type:varchar(16);not null" json:"job_type"`
	// Status is one of AIJobStatusProcessing | Done | Failed.
	Status string `gorm:"type:varchar(16);not null;index" json:"status"`
	// ResultPhotoURL holds the new photo URL once Status == done.
	ResultPhotoURL string `gorm:"type:text;not null;default:''" json:"result_photo_url"`
	// ErrorMessage holds a user-facing Spanish reason when Status ==
	// failed — Constitution Art. V.
	ErrorMessage string `gorm:"type:text;not null;default:''" json:"error_message"`
}

func (AIJob) TableName() string { return "ai_jobs" }

func (j *AIJob) BeforeCreate(tx *gorm.DB) error {
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now()
	}
	return nil
}
