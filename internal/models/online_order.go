package models

import "time"

type OnlineOrder struct {
	ID            string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	TenantID      string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	// Phase-6: pin every order to the sede that will fulfill it so
	// the KDS in that branch sees it and stock moves are scoped
	// correctly. Nullable pointer — legacy rows (pre-Phase-5) and
	// mono-sede tenants without a branch row still work.
	BranchID *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	CustomerName  string    `gorm:"not null" json:"customer_name"`
	CustomerPhone string    `gorm:"default:''" json:"customer_phone"`
	DeliveryType  string    `gorm:"default:'pickup'" json:"delivery_type"`
	// PaymentMethod is the free-form name selected by the customer
	// in the public catalog ("Efectivo", "Nequi Personal", etc.) —
	// the ID of the TenantPaymentMethod row is duplicated into
	// PaymentMethodID when the selection was from a configured
	// payment method, kept as a hint for receipts.
	PaymentMethod   string `gorm:"default:''" json:"payment_method"`
	PaymentMethodID string `gorm:"type:uuid;default:null" json:"payment_method_id,omitempty"`
	Status          string `gorm:"default:'pending'" json:"status"`
	TotalAmount     float64 `gorm:"default:0" json:"total_amount"`
	Items           string  `gorm:"type:jsonb;default:'[]'" json:"items"`
	Notes           string  `gorm:"default:''" json:"notes"`
}
