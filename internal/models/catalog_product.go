package models

import "time"

// CatalogProduct caches products from Open Food Facts to provide fast
// autocomplete without hitting the external API on every keystroke.
// Refreshed daily via background goroutine.
type CatalogProduct struct {
	ID        string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name      string    `gorm:"not null" json:"name"`
	Brand     string    `json:"brand,omitempty"`
	ImageURL  string    `json:"image_url,omitempty"`
	Barcode   string    `gorm:"index" json:"barcode,omitempty"`
	Category  string    `json:"category,omitempty"`
	FetchedAt time.Time `gorm:"not null;index" json:"-"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

func (CatalogProduct) TableName() string {
	return "catalog_products"
}
