package models

import "time"

type CatalogTemplate struct {
	BaseModel
	Name             string `gorm:"not null" json:"name"`
	BusinessType     string `gorm:"not null" json:"business_type"`
	PrimaryColorHex  string `gorm:"not null;default:'#000000'" json:"primary_color_hex"`
	DefaultBannerURL string `json:"default_banner_url"`
	IsActive         bool   `gorm:"not null;default:true" json:"is_active"`
}

type TenantCatalogConfig struct {
	TenantID      string    `gorm:"type:uuid;primaryKey" json:"tenant_id"`
	TemplateID    *string   `gorm:"type:uuid" json:"template_id"`
	CustomLogoURL string    `json:"custom_logo_url"`
	IsPublished   bool      `gorm:"not null;default:false" json:"is_published"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	Template *CatalogTemplate `gorm:"foreignKey:TemplateID" json:"template,omitempty"`
}

type CatalogAnalytics struct {
	BaseModel
	TenantID         string    `gorm:"type:uuid;not null;uniqueIndex" json:"tenant_id"`
	ViewsCount       int       `gorm:"default:0" json:"views_count"`
	OrdersGenerated  int       `gorm:"default:0" json:"orders_generated"`
	LastViewedAt     time.Time `json:"last_viewed_at"`

	// Join fields
	BusinessName string `gorm:"-" json:"business_name,omitempty"`
}

type CatalogAnalyticsDTO struct {
	TenantID        string  `json:"tenant_id"`
	BusinessName    string  `json:"business_name"`
	ViewsCount      int     `json:"views_count"`
	OrdersGenerated int     `json:"orders_generated"`
	ConversionRate  float64 `json:"conversion_rate"`
	LastViewedAt    time.Time `json:"last_viewed_at"`
}
