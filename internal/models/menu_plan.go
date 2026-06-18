// Spec: specs/066-planear-menu/spec.md
package models

// WeeklyMenuPlan — Spec 066. Plantilla semanal de menú. Ámbito por (tenant,
// sede): BranchID="" es el plan por defecto del comercio (single-sede y
// retrocompatibilidad); un BranchID concreto es el plan de esa sede. El índice
// único es compuesto (tenant_id, branch_id).
//
// Days es un JSONB con las claves de día (mon…sun); cada día tiene `enabled` y
// una lista de ítems `{recipe_uuid, planned_qty}`. planned_qty es SOLO guía de
// preparación del tendero (NO stock, NO viaja al público).
type WeeklyMenuPlan struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index:idx_wmp_tenant_branch,unique" json:"tenant_id"`
	// BranchID="" = plan por defecto del comercio (todas las sedes / single-sede).
	// NO es `type:uuid`: el centinela "" no es un UUID válido en Postgres y
	// `DEFAULT ''::uuid` haría fallar AutoMigrate. Se guarda como texto y se
	// compara como string (el branch real sigue siendo un UUID en su valor).
	BranchID string `gorm:"type:varchar(36);not null;default:'';index:idx_wmp_tenant_branch,unique" json:"branch_id"`
	Days     string `gorm:"type:jsonb;default:'{}'" json:"days"`
}

// MenuPlanOverride — Spec 066. Ajuste puntual por fecha que sobrescribe la
// plantilla de ese día solo para esa fecha (YYYY-MM-DD). Ámbito por (tenant,
// sede, fecha) garantizado por el índice único compuesto.
type MenuPlanOverride struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index:idx_mpo_tenant_branch_date,unique" json:"tenant_id"`
	// BranchID texto (no uuid): "" = sede por defecto; ver WeeklyMenuPlan.
	BranchID string `gorm:"type:varchar(36);not null;default:'';index:idx_mpo_tenant_branch_date,unique" json:"branch_id"`
	// Date se guarda como TEXTO YYYY-MM-DD (no `date` SQL): se compara
	// lexicográficamente y ese formato ordena correctamente, evitando el
	// round-trip a timestamp que hace el tipo `date` en GORM/Postgres.
	Date string `gorm:"not null;index:idx_mpo_tenant_branch_date,unique" json:"date"`
	// Enabled SIN `default:true`: GORM omite los bool en valor-cero (false)
	// cuando hay un default, aplicando el default en su lugar. Con
	// `default:false`, un día inhabilitado (false) persiste como false.
	Enabled bool   `gorm:"not null;default:false" json:"enabled"`
	Items   string `gorm:"type:jsonb;default:'[]'" json:"items"`
}
