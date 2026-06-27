// Spec: specs/084-peluqueria-salon/spec.md (Fase 2 — citas/turnos)
package models

import "time"

// Estados de una cita/turno.
const (
	AppointmentPending   = "pendiente"  // reservada por el cliente, sin confirmar
	AppointmentConfirmed = "confirmada" // el salón la confirmó
	AppointmentAttended  = "atendida"   // se realizó (se puede convertir en venta)
	AppointmentCancelled = "cancelada"
	AppointmentNoShow    = "no_show" // el cliente no llegó
)

// AppointmentSource — origen de la cita.
const (
	AppointmentSourcePublic = "public" // reservada desde el catálogo público
	AppointmentSourcePOS    = "pos"    // creada por el salón
)

// ValidAppointmentStatuses es la whitelist de estados.
var ValidAppointmentStatuses = map[string]struct{}{
	AppointmentPending:   {},
	AppointmentConfirmed: {},
	AppointmentAttended:  {},
	AppointmentCancelled: {},
	AppointmentNoShow:    {},
}

// Appointment — una cita/turno reservada para un servicio con un profesional en
// una franja horaria (peluquería/barbería, Fase 2). Aditivo y nullable; al
// atenderse se puede convertir en venta (SaleID) reusando la atribución por
// línea (EmployeeUUID) de la Fase 1.
type Appointment struct {
	BaseModel

	TenantID string  `gorm:"type:uuid;index;not null" json:"tenant_id"`
	BranchID *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`

	// Profesional asignado (nullable = "sin preferencia", lo asigna el salón).
	EmployeeUUID *string `gorm:"type:uuid;index" json:"employee_uuid,omitempty"`
	EmployeeName string  `gorm:"type:varchar(128);not null;default:''" json:"employee_name"`

	// Servicio reservado.
	ProductID   *string `gorm:"type:uuid;index" json:"product_id,omitempty"`
	ServiceName string  `gorm:"type:varchar(160);not null;default:''" json:"service_name"`
	Price       float64 `gorm:"type:numeric(12,2);not null;default:0" json:"price"`

	// Cliente (puede ser anónimo identificado solo por nombre/teléfono).
	CustomerID    *string `gorm:"type:uuid;index" json:"customer_id,omitempty"`
	CustomerName  string  `gorm:"type:varchar(128);not null;default:''" json:"customer_name"`
	CustomerPhone string  `gorm:"type:varchar(32);not null;default:''" json:"customer_phone"`

	StartsAt time.Time `gorm:"index;not null" json:"starts_at"`
	EndsAt   time.Time `gorm:"not null" json:"ends_at"`

	Status string `gorm:"type:varchar(16);not null;default:'pendiente';index" json:"status"`
	Source string `gorm:"type:varchar(8);not null;default:'public'" json:"source"`
	Notes  string `gorm:"type:text;not null;default:''" json:"notes"`

	// SaleID enlaza la cita atendida con la venta generada (idempotencia).
	SaleID *string `gorm:"type:uuid;index" json:"sale_id,omitempty"`
}
