package models

type EmergencyContact struct {
	BaseModel

	TenantID      string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name          string `gorm:"not null" json:"name"`
	PhoneNumber   string `gorm:"not null" json:"phone_number"`
	ContactMethod string `gorm:"not null;default:'whatsapp'" json:"contact_method"`
}
