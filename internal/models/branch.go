package models

type Branch struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name     string `gorm:"not null" json:"name"`
	Address  string `gorm:"default:''" json:"address"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
}
