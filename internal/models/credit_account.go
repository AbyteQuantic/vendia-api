package models

import "time"

type CreditAccount struct {
	BaseModel

	TenantID    string     `gorm:"type:uuid;index;not null" json:"tenant_id"`
	CreatedBy   string     `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID    string     `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	CustomerID  string     `gorm:"type:uuid;not null" json:"customer_id"`
	SaleID      *string    `gorm:"type:uuid" json:"sale_id,omitempty"`
	TotalAmount int64      `gorm:"not null" json:"total_amount"`
	PaidAmount  int64      `gorm:"default:0" json:"paid_amount"`
	Description string     `gorm:"default:''" json:"description"`
	Status      string     `gorm:"default:'open'" json:"status"`
	DueDate     *time.Time `json:"due_date"`

	// Fiado handshake fields
	FiadoToken  string     `gorm:"type:uuid;uniqueIndex" json:"fiado_token,omitempty"`
	FiadoStatus string     `gorm:"default:'none'" json:"fiado_status"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
	AcceptedIP  string     `gorm:"default:''" json:"accepted_ip,omitempty"`

	Customer Customer        `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Sale     Sale            `gorm:"foreignKey:SaleID" json:"sale,omitempty"`
	Payments []CreditPayment `gorm:"foreignKey:CreditAccountID" json:"payments,omitempty"`
}
