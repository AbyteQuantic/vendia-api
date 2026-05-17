// Spec: specs/008-planes-suscripcion-epayco/spec.md
package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SubscriptionCheckout persists one initiated ePayco checkout so the
// backend can later SERVE the checkout page for that reference.
//
// Why a table (Feature 008 reconciliation):
//
//	POST /subscription/checkout no longer hands the raw widget params
//	to the client — for a Flutter app (web + mobile) the client cannot
//	host the ePayco JS widget itself. Instead the backend persists the
//	checkout here and returns a URL: <base>/api/v1/subscription/pay/<ref>.
//	GET /subscription/pay/:ref then reads this row and renders an HTML
//	page that opens the official ePayco widget with these params.
//
// The row is the bridge between "checkout requested" and "checkout page
// served". It is NOT the source of truth for the payment — that stays
// the verified confirmation webhook (spec D2). It carries no money
// decision, only the data needed to render the widget.
//
// Additive (Art. X): a brand-new table, AutoMigrate-created, touching
// no existing schema. Reference is UNIQUE — it is also the public
// path segment, so a collision would serve the wrong checkout.
type SubscriptionCheckout struct {
	ID       string `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`

	// Reference is the unique checkout reference (x_id_invoice). It is
	// the public path segment of /subscription/pay/:ref.
	Reference string `gorm:"type:varchar(128);not null;uniqueIndex" json:"reference"`

	// Plan / Interval that was requested — echoed back by /checkout and
	// carried into the widget as extra2 / extra3.
	Plan     string `gorm:"type:varchar(16);not null" json:"plan"`
	Interval string `gorm:"type:varchar(16);not null" json:"interval"`

	// Amount is the COP amount to charge, integer-valued (Art. VII).
	Amount int `gorm:"not null" json:"amount"`

	// Description shown to the buyer inside the ePayco widget.
	Description string `gorm:"type:varchar(256);not null;default:''" json:"description"`

	// ResponseURL / ConfirmationURL are the absolute callbacks the
	// widget hands to ePayco. Stored at creation time so the served
	// page does not have to re-derive them.
	ResponseURL     string `gorm:"type:varchar(256);not null;default:''" json:"response_url"`
	ConfirmationURL string `gorm:"type:varchar(256);not null;default:''" json:"confirmation_url"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (SubscriptionCheckout) TableName() string { return "subscription_checkouts" }

func (s *SubscriptionCheckout) BeforeCreate(tx *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}
