package models

type CreditPayment struct {
	BaseModel

	CreditAccountID string `gorm:"type:uuid;index;not null" json:"credit_account_id"`
	Amount          int64  `gorm:"not null" json:"amount" binding:"required,gt=0"`
	PaymentMethod   string `gorm:"default:'cash'" json:"payment_method"`
	Note            string `json:"note"`
}
