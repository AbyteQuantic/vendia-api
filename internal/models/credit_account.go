package models

import "time"

type CreditAccount struct {
	BaseModel

	TenantID    string     `gorm:"type:uuid;index;not null" json:"tenant_id"`
	CustomerID  string     `gorm:"type:uuid;not null" json:"customer_id"`
	SaleID      string     `gorm:"type:uuid;not null" json:"sale_id"`
	TotalAmount int64      `gorm:"not null" json:"total_amount"`
	PaidAmount  int64      `gorm:"default:0" json:"paid_amount"`
	Status      string     `gorm:"default:'open'" json:"status"`
	DueDate     *time.Time `json:"due_date"`

	Customer Customer        `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Sale     Sale            `gorm:"foreignKey:SaleID" json:"sale,omitempty"`
	Payments []CreditPayment `gorm:"foreignKey:CreditAccountID" json:"payments,omitempty"`
}
