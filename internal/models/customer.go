package models

import "time"

// Customer is the tenant-owned CRM record we build silently as
// clients order from the public web catalogue. Fields added in the
// 2026-04 Habeas-Data epic (TermsAccepted, TermsAcceptedAt,
// LastOrderAt) are ADDITIVE — any previous row with these columns
// at their zero value is a valid "anonymous walk-in" customer.
//
// Privacy invariants (Colombian Ley 1581):
//   - Phone is the stable identifier across visits. We keep it
//     indexed+tenant-scoped to detect returning customers in O(1).
//   - Name is PII and MUST NEVER leak through a public endpoint
//     (e.g. /check-customer deliberately does not return it).
//   - TermsAccepted=true + TermsAcceptedAt set means the customer
//     has consented to the tenant's data-treatment policy at least
//     once. Re-prompt is only triggered when this is false/unset.
//   - MarketingOptIn is orthogonal: consent to store data != consent
//     to receive WhatsApp broadcasts.
type Customer struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name     string `gorm:"not null" json:"name" binding:"required,min=2"`
	Phone    string `gorm:"index" json:"phone"`
	Email    string `gorm:"default:''" json:"email"`
	Notes    string `json:"notes"`
	// MarketingOptIn gates WhatsApp broadcasts. False by default — the
	// customer must actively opt in before the app includes them in any
	// promotional blast. Required by Colombian Ley 1581 (Habeas Data).
	MarketingOptIn bool `gorm:"default:false" json:"marketing_opt_in"`

	// TermsAccepted is the Habeas-Data consent checkbox from the
	// public catalogue checkout. A false value means the next order
	// MUST present the consent UI again before we persist anything.
	TermsAccepted bool `gorm:"default:false" json:"terms_accepted"`

	// TermsAcceptedAt records WHEN consent was granted. Nullable
	// because legacy rows created before this epic won't have it, and
	// we don't want to back-fill a fake timestamp that would look like
	// real consent to an auditor.
	TermsAcceptedAt *time.Time `json:"terms_accepted_at,omitempty"`

	// LastOrderAt tracks the most recent successful order. Updated on
	// every upsert from PublicCreateOnlineOrder. Powers "returning
	// customer" detection and future cohort analytics — never exposed
	// publicly.
	LastOrderAt *time.Time `json:"last_order_at,omitempty"`
}
