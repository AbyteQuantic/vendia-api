// Spec: specs/104-moderacion-f1-lexico/spec.md
package models

// ModerationLog — registro AUDITABLE de cada decisión de moderación
// (exigible ante un requerimiento legal: qué se marcó, cuándo, por qué capa).
// Append-only: nunca se actualiza ni se borra desde la aplicación.
type ModerationLog struct {
	BaseModel

	TenantID   string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	EntityType string `gorm:"type:varchar(24);not null" json:"entity_type"` // product | broadcast_promotion
	EntityID   string `gorm:"type:uuid;index" json:"entity_id"`
	EntityName string `json:"entity_name"`
	Verdict    string `gorm:"type:varchar(16);not null" json:"verdict"` // review | blocked
	Category   string `gorm:"type:varchar(32)" json:"category"`
	// Actor: qué capa decidió — lexicon:f1 | gemini:f2 (futuro) | admin:<id>.
	Actor string `gorm:"type:varchar(64);not null" json:"actor"`
}
