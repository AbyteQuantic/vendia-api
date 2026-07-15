package models

type EmployeeRole string

const (
	RoleAdmin   EmployeeRole = "admin"
	RoleCashier EmployeeRole = "cashier"
	// Spec 105 F3 — roles de restaurante. Enum FIJO aditivo, UN rol por
	// empleado, presets de módulos fijos, CERO matriz de permisos.
	RoleWaiter EmployeeRole = "waiter" // mesero: mesas + entregar (+ cobrar si el tenant lo permite)
	RoleChef   EmployeeRole = "chef"   // cocina: SOLO comandas, nada de dinero
	// RoleCourier — Domiciliario: ETIQUETA sin vista propia en v1 (decisión
	// del fundador 2026-07-14); se trata como waiter en visibilidad.
	RoleCourier EmployeeRole = "courier"
)

// IsValidEmployeeRole — set cerrado del enum (Spec 105 F3: sin matriz de
// permisos, un rol por empleado).
func IsValidEmployeeRole(r EmployeeRole) bool {
	switch r {
	case RoleAdmin, RoleCashier, RoleWaiter, RoleChef, RoleCourier:
		return true
	}
	return false
}

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
	// PhotoURL is the staff member's profile photo. Feature 019: the
	// owner is just an Employee row with is_owner=true, so this single
	// column covers both the tendero (dueño) and every employee — no
	// separate table. Additive column picked up by AutoMigrate
	// (Constitution Art. X). Empty string = no photo yet.
	// Spec: specs/019-foto-perfil-tendero-empleado/spec.md
	PhotoURL string `gorm:"default:''" json:"photo_url"`
}
