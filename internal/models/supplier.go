package models

type Supplier struct {
	BaseModel

	TenantID    string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	CompanyName string `gorm:"not null" json:"company_name"`
	ContactName string `json:"contact_name,omitempty"`
	Phone       string `gorm:"not null" json:"phone"`
	Emoji       string `json:"emoji,omitempty"`
}
