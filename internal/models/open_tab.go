package models

import "time"

type OpenTab struct {
	BaseModel

	TenantID string     `gorm:"type:uuid;index;not null" json:"tenant_id"`
	TableID  string     `gorm:"type:uuid;not null" json:"table_id"`
	Status   string     `gorm:"default:'open'" json:"status"`
	Items    string     `gorm:"type:jsonb" json:"items"`
	OpenedAt time.Time  `gorm:"not null" json:"opened_at"`
	ClosedAt *time.Time `json:"closed_at"`
	SaleID   *string    `gorm:"type:uuid" json:"sale_id"`

	Table Table `gorm:"foreignKey:TableID" json:"table,omitempty"`
}
