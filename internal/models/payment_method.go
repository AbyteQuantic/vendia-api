package models

// TenantPaymentMethod represents a configured payment method for a business.
type TenantPaymentMethod struct {
	BaseModel

	TenantID       string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name           string `gorm:"not null" json:"name"`
	AccountDetails string `gorm:"default:''" json:"account_details"`
	IsActive       bool   `gorm:"default:true" json:"is_active"`
}

// TableName overrides GORM default to use payment_methods.
func (TenantPaymentMethod) TableName() string {
	return "payment_methods"
}
