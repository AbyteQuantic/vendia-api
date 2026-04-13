package models

type WorkspaceRole string

const (
	RoleOwner      WorkspaceRole = "owner"
	RoleWSAdmin    WorkspaceRole = "admin"
	RoleWSCashier  WorkspaceRole = "cashier"
	RoleWSWaiter   WorkspaceRole = "waiter"
)

type UserWorkspace struct {
	BaseModel

	UserID   string        `gorm:"type:uuid;index;not null" json:"user_id"`
	TenantID string        `gorm:"type:uuid;index;not null" json:"tenant_id"`
	BranchID *string       `gorm:"type:uuid" json:"branch_id,omitempty"`
	Role     WorkspaceRole `gorm:"not null;default:'owner'" json:"role"`
	IsDefault bool         `gorm:"default:false" json:"is_default"`

	// Preloaded relations (not stored)
	Tenant *Tenant `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
	Branch *Branch `gorm:"foreignKey:BranchID" json:"branch,omitempty"`
}
