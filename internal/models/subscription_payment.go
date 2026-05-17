package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Confirmed subscription payment (Stripe, manual, etc.) for revenue FinOps.
const (
	SubscriptionPaymentStatusConfirmed = "CONFIRMED"
	SubscriptionPaymentStatusPending   = "PENDING"
	SubscriptionPaymentStatusFailed    = "FAILED"
)

// SubscriptionPayment is a single collected PRO subscription payment.
//
// Feature 008 adds the ePayco columns. They are additive (Art. X):
// pre-008 rows leave them empty and the FinOps revenue queries that
// read AmountUSD / Status / ConfirmedAt keep working untouched.
//
//   - Amount / Currency: the COP amount actually charged (Art. VII —
//     money is exact; COP is stored as an integer-valued float).
//   - Plan / Interval: the billing.Plan id and cadence that was paid.
//   - EpaycoRef: our generated checkout reference (x_id_invoice).
//   - EpaycoTransactionID: ePayco's transaction id (x_transaction_id),
//     UNIQUE — this is the idempotency key. A re-sent confirmation hits
//     the unique index and is rejected before it can double-promote
//     the tenant or duplicate the row (FR-05 / AC-07). It is a *string
//     (nullable) ON PURPOSE: pre-008 rows have no ePayco id, and a
//     Postgres UNIQUE index ignores NULLs — so every legacy row coexists
//     while real transaction ids stay unique. An empty-string column
//     would instead collide on the second legacy row and break the
//     AutoMigrate deploy (Art. X — nullable column rule).
//   - PaidAt: when ePayco confirmed the transaction.
type SubscriptionPayment struct {
	ID          string  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID    string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	AmountUSD   float64 `gorm:"type:decimal(14,4);not null" json:"amount_usd"`
	Status      string  `gorm:"type:varchar(32);not null;index" json:"status"`
	ExternalRef string  `gorm:"type:varchar(256);not null;default:''" json:"external_ref,omitempty"`

	// ── ePayco columns (Feature 008) ──
	Amount              float64 `gorm:"type:decimal(14,2);not null;default:0" json:"amount"`
	Currency            string  `gorm:"type:varchar(8);not null;default:'COP'" json:"currency"`
	Plan                string  `gorm:"type:varchar(16);not null;default:''" json:"plan"`
	Interval            string  `gorm:"type:varchar(16);not null;default:''" json:"interval"`
	EpaycoRef           string  `gorm:"type:varchar(128);not null;default:'';index" json:"epayco_ref,omitempty"`
	EpaycoTransactionID *string `gorm:"type:varchar(128);uniqueIndex" json:"epayco_transaction_id,omitempty"`

	PaidAt      *time.Time `gorm:"index" json:"paid_at,omitempty"`
	ConfirmedAt *time.Time `gorm:"index" json:"confirmed_at,omitempty"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"not null" json:"updated_at"`
}

func (SubscriptionPayment) TableName() string { return "subscription_payments" }

func (p *SubscriptionPayment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}
