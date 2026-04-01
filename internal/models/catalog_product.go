package models

import "time"

// CatalogProduct is a shared, normalized product catalog across all tenants.
// Source "off" = imported from Open Food Facts, "user" = user-contributed with AI images.
type CatalogProduct struct {
	ID             string     `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name           string     `gorm:"not null" json:"name"`
	NormalizedName string     `json:"normalized_name,omitempty"`
	Brand          string     `json:"brand,omitempty"`
	ImageURL       string     `json:"image_url,omitempty"`
	Barcode        string     `gorm:"index" json:"barcode,omitempty"`
	SKU            string     `gorm:"index" json:"sku,omitempty"`
	Presentation   string     `json:"presentation,omitempty"`
	Content        string     `json:"content,omitempty"`
	Category       string     `json:"category,omitempty"`
	IsAIEnhanced   bool       `gorm:"default:false" json:"is_ai_enhanced"`
	Source         string     `gorm:"default:'off'" json:"source"`
	FetchedAt      *time.Time `gorm:"index" json:"-"`
	CreatedAt      time.Time  `json:"-"`
	UpdatedAt      time.Time  `json:"-"`
}

func (CatalogProduct) TableName() string {
	return "catalog_products"
}
