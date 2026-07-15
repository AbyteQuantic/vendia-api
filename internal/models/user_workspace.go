package models

type WorkspaceRole string

const (
	RoleOwner            WorkspaceRole = "owner"
	RoleWSAdmin          WorkspaceRole = "admin"
	RoleWSCashier        WorkspaceRole = "cashier"
	RoleWSWaiter         WorkspaceRole = "waiter"
	// Spec 105 F3 — chef/courier en el vocabulario workspace (viajan en el
	// claim `role` del JWT; el dashboard filtra módulos con esto).
	RoleWSChef    WorkspaceRole = "chef"
	RoleWSCourier WorkspaceRole = "courier"
	RoleWSInventoryMgr   WorkspaceRole = "inventory_manager"
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

// WorkspaceRoleForEmployee mapea el rol del empleado al vocabulario
// workspace (claim `role` del JWT). Spec 105 F3: waiter/chef/courier
// viajan tal cual; el resto conserva el mapeo histórico (owner>admin>cashier).
func WorkspaceRoleForEmployee(e Employee) WorkspaceRole {
	switch {
	case e.IsOwner:
		return RoleOwner
	case e.Role == RoleAdmin:
		return RoleWSAdmin
	case e.Role == RoleWaiter:
		return RoleWSWaiter
	case e.Role == RoleChef:
		return RoleWSChef
	case e.Role == RoleCourier:
		return RoleWSCourier
	default:
		return RoleWSCashier
	}
}
