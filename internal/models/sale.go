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
	// TaxAmount is the IVA (or other tax) charged on this sale.
	// Expressed in the same currency as Total. Zero for exempt tenants.
	TaxAmount float64 `gorm:"type:numeric(12,2);not null;default:0" json:"tax_amount"`
	// TipAmount is the optional propina (service/food businesses).
	// Kept separate from Total so reports can filter it out without
	// re-parsing line items.
	TipAmount     float64       `gorm:"type:numeric(12,2);not null;default:0" json:"tip_amount"`
	PaymentMethod PaymentMethod `gorm:"not null;default:'cash'" json:"payment_method"`
	CustomerID    *string       `gorm:"type:uuid" json:"customer_id,omitempty"`
	// CustomerNameSnapshot / CustomerPhoneSnapshot freeze the customer's
	// identity at the moment of sale. Reprinting an old receipt must not
	// depend on the Customer row still existing or still matching the
	// name the customer had at the time. Empty strings when there is no
	// customer attached.
	CustomerNameSnapshot  string `gorm:"type:varchar(128);not null;default:''" json:"customer_name_snapshot,omitempty"`
	CustomerPhoneSnapshot string `gorm:"type:varchar(32);not null;default:''" json:"customer_phone_snapshot,omitempty"`
	EmployeeUUID          *string `gorm:"type:uuid" json:"employee_uuid,omitempty"`
	EmployeeName          string  `json:"employee_name,omitempty"`
	ReceiptNumber         int64   `gorm:"index" json:"receipt_number,omitempty"`
	IsCredit              bool    `gorm:"default:false" json:"is_credit"`
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

// SaleItem is agnostic as of migration 020: it can represent either a
// product line (physical retail — ProductID populated, IsService=false)
// or a service line (ad-hoc billing — ProductID nil, IsService=true,
// CustomDescription + CustomUnitPrice populated). The database enforces
// the mutual exclusion via the `sale_items_product_or_service` CHECK.
type SaleItem struct {
	BaseModel

	SaleID    string  `gorm:"type:uuid;not null;index" json:"sale_id"`
	ProductID *string `gorm:"type:uuid" json:"product_id,omitempty"`
	Name              string  `gorm:"not null" json:"name"`
	Price             float64 `gorm:"not null" json:"price"`
	Quantity          int     `gorm:"not null;default:1" json:"quantity"`
	Subtotal          float64 `gorm:"not null" json:"subtotal"`
	IsContainerCharge bool    `gorm:"default:false" json:"is_container_charge"`
	// IsService toggles the line into service-billing mode. When true,
	// ProductID must be nil and CustomDescription must be filled.
	IsService         bool    `gorm:"not null;default:false" json:"is_service"`
	CustomDescription string  `gorm:"type:varchar(256);not null;default:''" json:"custom_description,omitempty"`
	CustomUnitPrice   float64 `gorm:"type:numeric(12,2);not null;default:0" json:"custom_unit_price,omitempty"`
}
