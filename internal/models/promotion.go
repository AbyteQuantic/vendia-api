package models

type Promotion struct {
	BaseModel

	TenantID    string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	ProductUUID string  `gorm:"type:uuid;not null" json:"product_uuid"`
	ProductName string  `gorm:"not null" json:"product_name"`
	OrigPrice   float64 `gorm:"not null" json:"orig_price"`
	PromoPrice  float64 `gorm:"not null" json:"promo_price"`
	PromoType   string  `gorm:"not null;default:'discount'" json:"promo_type"`
	Description string  `json:"description,omitempty"`
	IsActive    bool    `gorm:"default:true" json:"is_active"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
}
