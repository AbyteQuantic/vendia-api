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

	// Spec 096 — catálogo de fotos de referencia verificadas por barcode.
	// Status: "pending" (recién descubierta, aún no vetada) | "verified"
	// (imagen confirmada accesible, se puede sugerir) | "stale" (dejó de
	// responder en la re-verificación mensual, no se sugiere más).
	Status        string     `gorm:"default:'pending'" json:"status"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	// License / SourceURL: trazabilidad de la fuente para cumplimiento de
	// licencia (ej. "CC-BY-SA" + URL original en Open Food Facts). Solo
	// interno — nunca mostrado al tendero (decisión de /clarify).
	License   string `json:"license,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
}

func (CatalogProduct) TableName() string {
	return "catalog_products"
}
