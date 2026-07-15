// Spec: specs/105-hito-restaurante-comandas/spec.md — F5 (turno de caja con arqueo).
package models

import "time"

type CashShiftStatus string

const (
	CashShiftOpen   CashShiftStatus = "open"
	CashShiftClosed CashShiftStatus = "closed"
)

// CashShift — turno de caja con arqueo: el control antirrobo del dueño
// ausente (dolor #1 del concilio). Ciclo: abrir con base declarada →
// las ventas del turno se atan vía Sale.CashShiftUUID → cerrar contando
// el cajón → esperado (base + efectivo del turno) vs contado → la
// DIFERENCIA queda visible al dueño. Sin rostering ni nómina (v1).
type CashShift struct {
	BaseModel

	TenantID string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	BranchID *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`

	// Quién abrió (empleado del selector/PIN). Snapshot del nombre para
	// que el reporte sobreviva a renombres/bajas.
	OpenedBy     *string `gorm:"type:uuid" json:"opened_by,omitempty"`
	OpenedByName string  `json:"opened_by_name,omitempty"`

	Status CashShiftStatus `gorm:"not null;default:'open';index" json:"status"`

	// Base declarada al abrir (el sencillo del cajón).
	OpeningAmount float64   `gorm:"not null;default:0" json:"opening_amount"`
	OpenedAt      time.Time `gorm:"not null" json:"opened_at"`

	// Cierre: conteo físico vs esperado (base + ventas en EFECTIVO del
	// turno; lo digital no vive en el cajón). Difference = contado−esperado
	// (negativo = faltante). Punteros: null hasta cerrar.
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	ClosedBy       *string    `gorm:"type:uuid" json:"closed_by,omitempty"`
	CountedAmount  *float64   `json:"counted_amount,omitempty"`
	ExpectedAmount *float64   `json:"expected_amount,omitempty"`
	Difference     *float64   `json:"difference,omitempty"`
	Notes          string     `json:"notes,omitempty"`
}
