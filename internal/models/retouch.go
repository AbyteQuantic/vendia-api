// Spec: specs/101-retocar-fotos-inventario/spec.md
//
// Cola de retoque masivo de fotos (modo FIEL, Spec 094). Dos tablas nuevas,
// aditivas (Art. X): retouch_batches (un lote por tenant con backoff/fairness
// persistidos) y retouch_items (una foto del lote; el resultado queda en
// candidate_url SIN tocar products hasta que el tendero confirma — FR-05).
package models

import "time"

// Estados de un lote de retoque.
const (
	RetouchBatchStatusRunning     = "running"
	RetouchBatchStatusPausedError = "paused_error"
	RetouchBatchStatusCompleted   = "completed"
	RetouchBatchStatusCanceled    = "canceled"
)

// Estados de un ítem del lote. queued/processing/ready_for_review son los
// estados ACTIVOS (cubiertos por el índice UNIQUE parcial: un producto no
// vive en dos lotes activos a la vez — FR-13).
const (
	RetouchItemStatusQueued         = "queued"
	RetouchItemStatusProcessing     = "processing"
	RetouchItemStatusReadyForReview = "ready_for_review"
	RetouchItemStatusConfirmed      = "confirmed"
	RetouchItemStatusDiscarded      = "discarded"
	RetouchItemStatusFailed         = "failed"
	RetouchItemStatusSkippedStale   = "skipped_stale"
	RetouchItemStatusCanceled       = "canceled"
)

// RetouchBatch es un lote de retoque en segundo plano de UN tenant.
//   - LastServedAt: round-robin de equidad entre tenants (AC-12) — el worker
//     atiende siempre el lote menos recientemente servido.
//   - PausedUntil: backoff persistido — 429 del proveedor (pausa global que
//     sobrevive reinicios, AC-10) o circuit breaker paused_error.
//
// Sin contadores desnormalizados: el progreso (queued/processed/failed/
// ready_for_review) se recalcula SIEMPRE de retouch_items con GROUP BY en el
// summary — una sola fuente de verdad, nada que se desincronice.
type RetouchBatch struct {
	BaseModel

	TenantID     string     `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Status       string     `gorm:"default:'running';index" json:"status"`
	LastServedAt *time.Time `json:"last_served_at,omitempty"`
	PausedUntil  *time.Time `json:"paused_until,omitempty"`
}

// RetouchItem es una foto dentro de un lote.
//   - SourcePhotoURL: snapshot de la foto al encolar — si la foto ACTUAL del
//     producto ya no coincide al procesar, el ítem sale como skipped_stale
//     (idempotencia capa 2, FR-13).
//   - CandidateURL: el resultado -enhanced subido a R2, NO aplicado al
//     producto; solo el confirm del tendero lo vuelve photo_url (FR-05).
type RetouchItem struct {
	BaseModel

	BatchID        string     `gorm:"type:uuid;not null;index:idx_retouch_items_batch_status" json:"batch_id"`
	TenantID       string     `gorm:"type:uuid;not null;index" json:"tenant_id"`
	ProductID      string     `gorm:"type:uuid;not null;index" json:"product_id"`
	SourcePhotoURL string     `json:"source_photo_url"`
	CandidateURL   string     `json:"candidate_url,omitempty"`
	Status         string     `gorm:"default:'queued';index:idx_retouch_items_batch_status" json:"status"`
	Attempts       int        `gorm:"default:0" json:"attempts"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
}
