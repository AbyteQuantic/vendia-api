package models

type PaymentMethod string

const (
	PaymentCash     PaymentMethod = "cash"
	PaymentTransfer PaymentMethod = "transfer"
	PaymentCard     PaymentMethod = "card"
	PaymentCredit   PaymentMethod = "credit"
)

type Sale struct {
	BaseModel

	TenantID      string        `gorm:"type:uuid;not null;index" json:"tenant_id"`
	CreatedBy     *string       `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID      *string       `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Total         float64       `gorm:"not null" json:"total"`
	PaymentMethod PaymentMethod `gorm:"not null;default:'cash'" json:"payment_method"`
	CustomerID    *string       `gorm:"type:uuid" json:"customer_id,omitempty"`
	EmployeeUUID  *string       `gorm:"type:uuid" json:"employee_uuid,omitempty"`
	EmployeeName  string        `json:"employee_name,omitempty"`
	ReceiptNumber int64         `gorm:"index" json:"receipt_number,omitempty"`
	IsCredit      bool          `gorm:"default:false" json:"is_credit"`
	// CreditAccountID links this sale to a specific open fiado so the public
	// acceptance page can show itemized detail per debt entry. Nullable for
	// cash/transfer/card sales.
	CreditAccountID *string `gorm:"type:uuid;index" json:"credit_account_id,omitempty"`
	// PaymentStatus supports the zero-fee dynamic-QR flow: COMPLETED is
	// the default for cash + credit sales, transfer-with-QR sales go
	// through PENDING until the cashier manually confirms receipt of the
	// Nequi/Daviplata/Bancolombia transfer.
	PaymentStatus string `gorm:"type:varchar(32);not null;default:'COMPLETED';index" json:"payment_status"`
	// DynamicQRPayload is the raw QR string shown to the customer for
	// transfer sales (e.g. "nequi://pay?phone=300…&amount=12500"). Kept
	// for audit + later reconciliation when Webhooks land.
	DynamicQRPayload *string    `gorm:"type:text" json:"dynamic_qr_payload,omitempty"`
	Items            []SaleItem `gorm:"foreignKey:SaleID" json:"items"`
}

type SaleItem struct {
	BaseModel

	SaleID            string  `gorm:"type:uuid;not null;index" json:"sale_id"`
	ProductID         string  `gorm:"type:uuid;not null" json:"product_id"`
	Name              string  `gorm:"not null" json:"name"`
	Price             float64 `gorm:"not null" json:"price"`
	Quantity          int     `gorm:"not null;default:1" json:"quantity"`
	Subtotal          float64 `gorm:"not null" json:"subtotal"`
	IsContainerCharge bool    `gorm:"default:false" json:"is_container_charge"`
}
