// Spec: specs/042-modulo-eventos/spec.md
package models

import "time"

// Payment statuses for an inscription. The cupo is only consumed when a
// registration reaches "confirmed" (spec FR-09, decision #7).
const (
	RegistrationPaymentPending   = "pending"
	RegistrationPaymentConfirmed = "confirmed"
	RegistrationPaymentFailed    = "failed"
	RegistrationPaymentCancelled = "cancelled"
)

// EventRegistration is one attendee's inscription to an event. The attendee
// is always materialized as a Customer of the organizer (spec FR-07); the
// installment plan, when used, points at a SEPARATE credit account tied to
// this registration (decision R-02) — never the store fiado account.
type EventRegistration struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	EventID  string `gorm:"type:uuid;not null;index" json:"event_id"`
	// CustomerID is the organizer's Customer (created/deduped on register).
	CustomerID string `gorm:"type:uuid;not null;index" json:"customer_id"`

	// FormData holds the answers to the event's custom inscription fields.
	FormData map[string]any `gorm:"serializer:json;type:jsonb;default:'{}'" json:"form_data"`

	// ConsentCommsAt records the mandatory Habeas-Data consent timestamp for
	// the organizer's WhatsApp/email communications (spec FR-08, AC-07).
	ConsentCommsAt *time.Time `json:"consent_comms_at,omitempty"`

	PaymentMethod string `json:"payment_method,omitempty"`
	PaymentStatus string `gorm:"not null;default:'pending';index" json:"payment_status"`

	// CreditAccountID links to the event-scoped fiado account when the
	// attendee pays in manual installments (nullable — Art. X *string rule).
	CreditAccountID *string `gorm:"type:uuid;index" json:"credit_account_id,omitempty"`

	// QRToken is the unguessable identifier embedded in the badge QR and
	// scanned at check-in/out. PublicToken backs the public enrollment portal.
	QRToken     string `gorm:"type:uuid;uniqueIndex" json:"qr_token"`
	PublicToken string `gorm:"type:uuid;uniqueIndex" json:"public_token"`

	// CertificateEligible flips true when the attendance rule is satisfied;
	// issuance stays manual (spec FR-16/FR-17).
	CertificateEligible bool       `gorm:"default:false" json:"certificate_eligible"`
	CertificateIssuedAt *time.Time `json:"certificate_issued_at,omitempty"`
}

// IsConfirmed reports whether this registration consumes a cupo.
func (r *EventRegistration) IsConfirmed() bool {
	return r.PaymentStatus == RegistrationPaymentConfirmed
}
