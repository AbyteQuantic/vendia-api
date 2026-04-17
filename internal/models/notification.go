package models

import "time"

type Notification struct {
	ID        string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	TenantID  string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Title     string    `gorm:"not null" json:"title"`
	Body      string    `gorm:"default:''" json:"body"`
	Type      string    `gorm:"default:'info'" json:"type"`
	IsRead    bool      `gorm:"default:false" json:"is_read"`
}
