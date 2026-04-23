package models

type EmployeeRole string

const (
	RoleAdmin   EmployeeRole = "admin"
	RoleCashier EmployeeRole = "cashier"
)

type Employee struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	// BranchID scopes the employee to a specific sede. Nullable at
	// the DB layer (migration 025) so legacy rows backfill cleanly,
	// but handlers reject creates without a valid value — the
	// NOT NULL invariant lives at the application layer so large
	// tenants don't pay for a table rewrite just to catch a bug
	// that a 400 response handles cheaper. Pointer so JSON marshals
	// to `null` instead of an empty-string sentinel that breaks
	// UUID casts in Postgres.
	BranchID     *string      `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Name         string       `gorm:"not null" json:"name"`
	Phone        string       `gorm:"index" json:"phone,omitempty"`
	Pin          string       `gorm:"size:60" json:"-"`
	Role         EmployeeRole `gorm:"not null;default:'cashier'" json:"role"`
	PasswordHash string       `gorm:"not null" json:"-"`
	IsOwner      bool         `gorm:"default:false" json:"is_owner"`
	IsActive     bool         `gorm:"default:true" json:"is_active"`
}
