package models

import "time"

type RefreshToken struct {
	BaseModel

	TenantID  string    `gorm:"type:uuid;not null;index" json:"tenant_id"`
	UserID    *string   `gorm:"type:uuid;index" json:"user_id,omitempty"`
	Token     string    `gorm:"not null;uniqueIndex;size:64" json:"-"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	Revoked   bool      `gorm:"not null;default:false" json:"revoked"`
}
