package models

type CreditPayment struct {
	BaseModel

	CreditAccountID string `gorm:"type:uuid;index;not null" json:"credit_account_id"`
	CreatedBy       string `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID        string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Amount          int64  `gorm:"not null" json:"amount" binding:"required,gt=0"`
	PaymentMethod   string `gorm:"default:'cash'" json:"payment_method"`
	Note            string `json:"note"`
}
