// Spec: specs/084-peluqueria-salon/spec.md
package models

import "time"

// Modelos de pago al profesional (decisión fundador 2026-06-27).
const (
	PayModelCommission       = "commission"        // % por servicio
	PayModelFixedPerJob      = "fixed_per_job"      // fijo por trabajo/turno
	PayModelChairRent        = "chair_rent"         // arriendo de silla
	PayModelSalaryCommission = "salary_commission"  // sueldo base + comisión
)

// PayBasis — qué modelo aplicó a una LÍNEA al cobrar (snapshot en SaleItem).
const (
	PayBasisNone       = "none"
	PayBasisCommission = "commission"
	PayBasisFixed      = "fixed"
	PayBasisChairRent  = "chair_rent"
)

// FixedUnit / RentUnit / WhoCollects vocab.
const (
	FixedUnitService = "service"
	FixedUnitTicket  = "ticket"
	FixedUnitDay     = "day"

	RentUnitDaily  = "daily"
	RentUnitWeekly = "weekly"

	WhoCollectsShop = "shop"
	WhoCollectsPro  = "pro"
)

// ValidPayModels es la whitelist de modelos de pago.
var ValidPayModels = map[string]struct{}{
	PayModelCommission:       {},
	PayModelFixedPerJob:      {},
	PayModelChairRent:        {},
	PayModelSalaryCommission: {},
}

// EmployeePayConfig — cómo se le paga a un profesional. UNA fila activa por
// (tenant, empleado, sede); effective-dated (al cambiar el esquema se desactiva
// la previa y se crea otra, conservando el historial para liquidaciones pasadas).
// Spec 084.
type EmployeePayConfig struct {
	BaseModel

	TenantID     string  `gorm:"type:uuid;index;not null" json:"tenant_id"`
	BranchID     *string `gorm:"type:uuid;index" json:"branch_id,omitempty"` // nullable = config a nivel tenant
	EmployeeUUID string  `gorm:"type:uuid;index;not null" json:"employee_uuid"`

	PayModel string `gorm:"type:varchar(24);not null;default:'commission'" json:"pay_model"`

	// Comisión (modelos commission / salary_commission).
	CommissionPct *float64 `gorm:"type:numeric(5,2)" json:"commission_pct,omitempty"`
	// Fijo por trabajo/turno.
	FixedPerJob *float64 `gorm:"type:numeric(12,2)" json:"fixed_per_job,omitempty"`
	FixedUnit   string   `gorm:"type:varchar(16);not null;default:'service'" json:"fixed_unit"`
	// Sueldo base (modelo salary_commission), por periodo.
	BaseSalary *float64 `gorm:"type:numeric(12,2)" json:"base_salary,omitempty"`
	// Arriendo de silla.
	RentRate    *float64 `gorm:"type:numeric(12,2)" json:"rent_rate,omitempty"`
	RentUnit    string   `gorm:"type:varchar(16);not null;default:'daily'" json:"rent_unit"`
	WhoCollects string   `gorm:"type:varchar(8);not null;default:'shop'" json:"who_collects"`
	// Propina: fracción que se queda el profesional (1.0 = 100%).
	TipRate float64 `gorm:"type:numeric(5,4);not null;default:1" json:"tip_rate"`

	EffectiveFrom time.Time `gorm:"not null" json:"effective_from"`
	IsActive      bool      `gorm:"not null;default:true" json:"is_active"`
}
