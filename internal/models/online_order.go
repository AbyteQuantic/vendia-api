package models

import "time"

type OnlineOrder struct {
	ID            string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	TenantID      string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	CustomerName  string    `gorm:"not null" json:"customer_name"`
	CustomerPhone string    `gorm:"default:''" json:"customer_phone"`
	DeliveryType  string    `gorm:"default:'pickup'" json:"delivery_type"`
	Status        string    `gorm:"default:'pending'" json:"status"`
	TotalAmount   float64   `gorm:"default:0" json:"total_amount"`
	Items         string    `gorm:"type:jsonb;default:'[]'" json:"items"`
	Notes         string    `gorm:"default:''" json:"notes"`
}
