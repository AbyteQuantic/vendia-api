package models

type CreditPayment struct {
	BaseModel

	CreditAccountID string  `gorm:"type:uuid;index;not null" json:"credit_account_id"`
	CreatedBy       *string `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID        *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Amount          int64   `gorm:"not null" json:"amount" binding:"required,gt=0"`
	PaymentMethod   string  `gorm:"default:'cash'" json:"payment_method"`
	Note            string  `json:"note"`
	// ReceiptImageURL — same contract as Sale.ReceiptImageURL but for
	// abonos to a fiado. Stored alongside the payment so the audit
	// trail survives even after the storage TTL expires (only the
	// image is deleted; the URL persists as evidence the cashier did
	// present a comprobante at the time).
	ReceiptImageURL string `gorm:"type:text;default:''" json:"receipt_image_url"`
}
