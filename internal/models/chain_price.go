// Spec: specs/077-compra-inteligente-insumos/spec.md
package models

import "time"

// ChainPrice — el GRAN catálogo de precios scrapeados de cadenas (Éxito,
// Olímpica…), APPEND-ONLY por ciudad. Es referencia (no garantizado): cada
// pasada del cron inserta filas nuevas con ScrapedAt → historial para detectar
// bajadas de precio. NO es per-tenant (es data pública compartida). Spec 077.
type ChainPrice struct {
	BaseModel

	Chain string `gorm:"type:varchar(40);index;not null" json:"chain"` // exito | olimpica
	City  string `gorm:"type:varchar(120);index;default:''" json:"city"`

	RawName        string `gorm:"not null" json:"raw_name"`
	NormalizedName string `gorm:"index;not null" json:"normalized_name"` // para el match
	Brand          string `gorm:"default:''" json:"brand"`

	Price            float64 `gorm:"not null" json:"price"`
	ListPrice        float64 `gorm:"default:0" json:"list_price"`
	Unit             string  `gorm:"type:varchar(16);default:''" json:"unit"`
	PackQty          float64 `gorm:"default:0" json:"pack_qty"`
	PricePerBaseUnit float64 `gorm:"default:0" json:"price_per_base_unit"`

	Category string `gorm:"default:''" json:"category"`
	SKU      string `gorm:"type:varchar(64);default:''" json:"sku"`
	URL      string `gorm:"default:''" json:"url"`

	ScrapedAt time.Time `gorm:"index;not null" json:"scraped_at"`
}
