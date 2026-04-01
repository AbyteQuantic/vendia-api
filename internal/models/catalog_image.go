package models

import "time"

// CatalogImage stores AI-generated product images linked to catalog products.
// Max 3 accepted images per catalog product.
type CatalogImage struct {
	ID                string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CatalogProductID  string    `gorm:"type:uuid;not null;index" json:"catalog_product_id"`
	ImageURL          string    `gorm:"not null" json:"image_url"`
	StorageKey        string    `gorm:"not null" json:"-"`
	CreatedByTenantID string    `gorm:"type:uuid;not null" json:"created_by_tenant_id"`
	IsAccepted        bool      `gorm:"default:false" json:"is_accepted"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (CatalogImage) TableName() string {
	return "catalog_images"
}
