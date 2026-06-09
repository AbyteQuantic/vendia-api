// Spec: specs/042-modulo-eventos/spec.md
package models

import "time"

// Payment proof review states. A guest submits a manual-payment proof
// (transfer/cash receipt) that starts "pending"; the organizer reviews it and
// "approves" it, which records the abono against the registration.
const (
	EventPaymentPending  = "pending"
	EventPaymentApproved = "approved"
)

// EventPayment is one money movement against an inscription: either a proof the
// attendee uploaded for a manual payment (transfer/cash), or an abono the
// organizer registered directly. Approving a pending proof feeds the amount
// into the registration's running total (and activates the carné when complete)
// — no online gateway involved (decision: manual con comprobante).
type EventPayment struct {
	BaseModel

	TenantID       string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	EventID        string `gorm:"type:uuid;not null;index" json:"event_id"`
	RegistrationID string `gorm:"type:uuid;not null;index" json:"registration_id"`

	// Amount the attendee reports (or the organizer enters), in COP.
	Amount int64 `gorm:"not null" json:"amount"`

	// ProofURL is the uploaded receipt image (R2 url or data URL). Empty for an
	// organizer-entered abono with no attachment.
	ProofURL string `json:"proof_url,omitempty"`
	Note     string `json:"note,omitempty"`

	// Status: pending (awaiting review) → approved (counted).
	Status     string     `gorm:"not null;default:'pending';index" json:"status"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

// SetIdentity stamps the authoritative id and tenant_id during offline sync.
func (p *EventPayment) SetIdentity(id, tenantID string) {
	p.ID = id
	p.TenantID = tenantID
}
