package models

import "time"

// InvoiceLog records each successfully saved invoice scan for the
// owner's audit trail. Lightweight: no FK to products, just a text
// summary so it survives product deletions.
type InvoiceLog struct {
	ID           string  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID     string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	BranchID     *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	UserID       *string `gorm:"type:uuid" json:"user_id,omitempty"`
	UserName     string  `gorm:"type:varchar(128)" json:"user_name,omitempty"`
	ProviderName string  `gorm:"type:varchar(256)" json:"provider_name"`
	ProductCount int     `json:"product_count"`
	CreatedCount int     `json:"created_count"`
	UpdatedCount int     `json:"updated_count"`
	InvoiceTotal float64 `json:"invoice_total"`
	Summary      string  `gorm:"type:text" json:"summary"`
	CreatedAt    time.Time `json:"created_at"`
}
