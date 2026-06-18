// Spec: specs/066-planear-menu/spec.md
package models

// WeeklyMenuPlan — Spec 066. Plantilla semanal de menú del comercio. MVP por
// TENANT (un registro por comercio): el link público es por-tenant, así que un
// plan por comercio evita la ambigüedad de "¿qué sede muestra el link?". La
// migración a branch_id queda como follow-up aditivo cuando lleguen las URLs
// por sede.
//
// Days es un JSONB con las claves de día de la semana (mon…sun); cada día tiene
// `enabled` y una lista de ítems `{recipe_uuid, planned_qty}`. planned_qty es
// SOLO guía de preparación del tendero (NO stock, NO viaja al público).
type WeeklyMenuPlan struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;uniqueIndex" json:"tenant_id"`
	Days     string `gorm:"type:jsonb;default:'{}'" json:"days"`
}

// MenuPlanOverride — Spec 066. Ajuste puntual por fecha que sobrescribe la
// plantilla de ese día solo para esa fecha (YYYY-MM-DD). Un registro por
// (tenant, fecha) garantizado por el índice único compuesto.
type MenuPlanOverride struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index:idx_mpo_tenant_date,unique" json:"tenant_id"`
	// Date se guarda como TEXTO YYYY-MM-DD (no `date` SQL): se compara
	// lexicográficamente y ese formato ordena correctamente, evitando el
	// round-trip a timestamp que hace el tipo `date` en GORM/Postgres.
	Date string `gorm:"not null;index:idx_mpo_tenant_date,unique" json:"date"`
	// Enabled SIN `default:true`: GORM omite los bool en valor-cero (false)
	// cuando hay un default, aplicando el default en su lugar. Con
	// `default:false`, un día inhabilitado (false) persiste como false.
	Enabled bool   `gorm:"not null;default:false" json:"enabled"`
	Items   string `gorm:"type:jsonb;default:'[]'" json:"items"`
}
