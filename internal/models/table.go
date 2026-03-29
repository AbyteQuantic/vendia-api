package models

type Table struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Label    string `gorm:"not null" json:"label" binding:"required"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
}
