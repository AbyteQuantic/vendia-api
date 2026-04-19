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
	CreatedBy     string        `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID      string        `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Total         float64       `gorm:"not null" json:"total"`
	PaymentMethod PaymentMethod `gorm:"not null;default:'cash'" json:"payment_method"`
	CustomerID    *string       `gorm:"type:uuid" json:"customer_id,omitempty"`
	EmployeeUUID  string        `gorm:"type:uuid" json:"employee_uuid,omitempty"`
	EmployeeName  string        `json:"employee_name,omitempty"`
	ReceiptNumber int64         `gorm:"index" json:"receipt_number,omitempty"`
	IsCredit      bool          `gorm:"default:false" json:"is_credit"`
	Items         []SaleItem    `gorm:"foreignKey:SaleID" json:"items"`
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
