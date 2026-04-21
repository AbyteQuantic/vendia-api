package models

type Customer struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name     string `gorm:"not null" json:"name" binding:"required,min=2"`
	Phone    string `gorm:"index" json:"phone"`
	Email    string `gorm:"default:''" json:"email"`
	Notes    string `json:"notes"`
	// MarketingOptIn gates WhatsApp broadcasts. False by default — the
	// customer must actively opt in before the app includes them in any
	// promotional blast. Required by Colombian Ley 1581 (Habeas Data).
	MarketingOptIn bool `gorm:"default:false" json:"marketing_opt_in"`
}
