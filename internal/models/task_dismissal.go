// Spec: specs/078-centro-tareas-unificado/spec.md
package models

import "time"

// TaskDismissal — posponer ("snooze") una tarea AGREGADA sin máquina de estados
// propia (reorder/perishable). Las tareas con entidad propia derivan su estado de
// la entidad real y NO necesitan esto. Única tabla nueva del Spec 078.
// uuid como varchar(36) (no DEFAULT ”) por la lección de AutoMigrate del Spec 066.
type TaskDismissal struct {
	// PK determinista "{tenant}:{task_id}" (upsert renueva el plazo) → ancho para
	// caber tenant(36)+task_id; varchar (no uuid DEFAULT '') por lección Spec 066.
	ID             string    `gorm:"type:varchar(160);primaryKey" json:"id"`
	TenantID       string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	TaskID         string    `gorm:"type:varchar(96);index;not null" json:"task_id"` // "{kind}:{source_id}"
	DismissedUntil time.Time `json:"dismissed_until"`
	CreatedAt      time.Time `json:"created_at"`
}
