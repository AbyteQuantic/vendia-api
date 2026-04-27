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

// SubscriptionPayment is a single collected PRO subscription payment in USD.
type SubscriptionPayment struct {
	ID          string     `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID    string     `gorm:"type:uuid;not null;index" json:"tenant_id"`
	AmountUSD   float64    `gorm:"type:decimal(14,4);not null" json:"amount_usd"`
	Status      string     `gorm:"type:varchar(32);not null;index" json:"status"`
	ExternalRef string     `gorm:"type:varchar(256);not null;default:''" json:"external_ref,omitempty"`
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
