package models

type Customer struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name     string `gorm:"not null" json:"name" binding:"required,min=2"`
	Phone    string `gorm:"index" json:"phone"`
	Email    string `gorm:"default:''" json:"email"`
	Notes    string `json:"notes"`
}
