package models

type Branch struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name     string `gorm:"not null" json:"name"`
	Address  string `gorm:"default:''" json:"address"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
	// IsDefault marks the sede the frontend selects on login when the
	// employee has no branch of their own assigned (typically the
	// owner). Exactly one per tenant — set on the "Principal" branch
	// at registration; backfilled for pre-existing tenants in
	// database.BackfillDefaultBranch. Without a real default, the
	// frontend's `firstWhere(isDefault, orElse: first)` silently falls
	// back to array order, which can select an empty sede over the one
	// holding the tenant's actual inventory (Spec: incident 2026-07-05).
	IsDefault bool `gorm:"not null;default:false" json:"is_default"`
}
