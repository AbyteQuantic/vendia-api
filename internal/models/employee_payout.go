// Spec: specs/084-peluqueria-salon/spec.md
package models

import "time"

// Dirección y tipo del pago.
const (
	PayoutDirectionToPro   = "to_pro"   // el salón le paga al profesional (comisión/sueldo)
	PayoutDirectionToSalon = "to_salon" // el profesional le paga al salón (arriendo)

	PayoutKindLiquidacion = "liquidacion"
	PayoutKindAnticipo    = "anticipo"
	PayoutKindArriendo    = "arriendo"

	PayoutStatusDraft = "draft"
	PayoutStatusPaid  = "paid"
	PayoutStatusVoid  = "void"
)

// EmployeePayout — registro APPEND-ONLY de una liquidación/anticipo/arriendo
// pagado a (o cobrado de) un profesional. Es el "registro" legal/contable: guarda
// el desglose snapshot del periodo. Nunca se borra; se anula con Status=void.
// Spec 084.
type EmployeePayout struct {
	BaseModel

	TenantID     string  `gorm:"type:uuid;index;not null" json:"tenant_id"`
	BranchID     *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	EmployeeUUID string  `gorm:"type:uuid;index;not null" json:"employee_uuid"`
	EmployeeName string  `gorm:"type:varchar(128);not null;default:''" json:"employee_name"`

	PeriodStart time.Time `gorm:"not null" json:"period_start"`
	PeriodEnd   time.Time `gorm:"not null" json:"period_end"`
	PayModel    string    `gorm:"type:varchar(24);not null;default:'commission'" json:"pay_model"`
	Direction   string    `gorm:"type:varchar(8);not null;default:'to_pro'" json:"direction"`
	Kind        string    `gorm:"type:varchar(16);not null;default:'liquidacion'" json:"kind"`

	// Desglose (COP). GrossServices = suma de servicios atribuidos en el periodo.
	GrossServices   float64 `gorm:"type:numeric(12,2);not null;default:0" json:"gross_services"`
	ServiceCount    int     `gorm:"not null;default:0" json:"service_count"`
	CommissionAmount float64 `gorm:"type:numeric(12,2);not null;default:0" json:"commission_amount"`
	FixedAmount     float64 `gorm:"type:numeric(12,2);not null;default:0" json:"fixed_amount"`
	SalaryAmount    float64 `gorm:"type:numeric(12,2);not null;default:0" json:"salary_amount"`
	ChairRentAmount float64 `gorm:"type:numeric(12,2);not null;default:0" json:"chair_rent_amount"`
	TipAmount       float64 `gorm:"type:numeric(12,2);not null;default:0" json:"tip_amount"`
	// NetPayout puede ser NEGATIVO (arriendo > recaudo → el profesional debe al salón).
	NetPayout     float64 `gorm:"type:numeric(12,2);not null;default:0" json:"net_payout"`
	RoundingDelta float64 `gorm:"type:numeric(12,2);not null;default:0" json:"rounding_delta"`

	Method string     `gorm:"type:varchar(32);not null;default:''" json:"method"`
	Status string     `gorm:"type:varchar(16);not null;default:'paid'" json:"status"`
	Notes  string     `gorm:"type:text;not null;default:''" json:"notes"`
	PaidAt *time.Time `json:"paid_at,omitempty"`
}
