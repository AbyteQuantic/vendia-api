package models

type EmployeeRole string

const (
	RoleAdmin   EmployeeRole = "admin"
	RoleCashier EmployeeRole = "cashier"
)

type Employee struct {
	BaseModel

	TenantID     string       `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Name         string       `gorm:"not null" json:"name"`
	Phone        string       `gorm:"index" json:"phone,omitempty"`
	Pin          string       `gorm:"size:60" json:"-"`
	Role         EmployeeRole `gorm:"not null;default:'cashier'" json:"role"`
	PasswordHash string       `gorm:"not null" json:"-"`
	IsOwner      bool         `gorm:"default:false" json:"is_owner"`
	IsActive     bool         `gorm:"default:true" json:"is_active"`
}
