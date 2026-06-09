// Spec: specs/042-modulo-eventos/spec.md (§12 D3)
package models

import "time"

// Installment statuses (Spanish — surfaced to the organizer; spec §8).
const (
	InstallmentStatusPending = "pendiente"
	InstallmentStatusPaid    = "pagada"
	InstallmentStatusOverdue = "vencida"
)

// EventInstallment is one dated cuota of an event registration's manual
// payment plan. Decision D3: the schedule is PERSISTED with a due date per
// cuota (not computed on the fly) so reminders for "cuota próxima/vencida"
// are precise (FR-10/FR-20). The amounts still sum exactly to the event price
// and each is a multiple of $50 (Art. VII).
type EventInstallment struct {
	BaseModel

	TenantID        string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	RegistrationID  string  `gorm:"type:uuid;not null;index" json:"registration_id"`
	CreditAccountID *string `gorm:"type:uuid;index" json:"credit_account_id,omitempty"`

	Number  int       `gorm:"not null" json:"number"`
	Amount  int64     `gorm:"not null" json:"amount"`
	DueDate time.Time `gorm:"index" json:"due_date"`
	Status  string    `gorm:"not null;default:'pendiente'" json:"status"`
}

// SetIdentity stamps the authoritative id and tenant_id during offline sync
// (Art. III) — implements the tenantScoped contract used by sync_service.
func (i *EventInstallment) SetIdentity(id, tenantID string) {
	i.ID = id
	i.TenantID = tenantID
}
