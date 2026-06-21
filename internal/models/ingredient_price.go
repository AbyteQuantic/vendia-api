// Spec: specs/077-compra-inteligente-insumos/spec.md
package models

import "time"

// Fuentes de precio (prioridad/confianza descendente al leer el sugerido).
const (
	PriceSourceVendiaCatalog = "vendia_catalog" // catálogo de un proveedor en la red VendIA
	PriceSourceManual        = "manual"         // el tenant lo escribió
	PriceSourceInvoiceOCR    = "invoice_ocr"    // extraído de una factura escaneada
	PriceSourceScrapedChain  = "scraped_chain"  // scraping de cadena (referencia)
)

// IngredientPrice — conocimiento de precio de un insumo, APPEND-ONLY (cada
// captura es una fila → historial + auditoría gratis). El precio SUGERIDO NO se
// persiste: se calcula en lectura por prioridad de fuente + frescura. Spec 077.
type IngredientPrice struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	// BranchID scope-sede (gotcha Spec 014): varchar(36) con centinela '' para
	// que AutoMigrate no rompa en Postgres (gotcha Spec 066).
	BranchID string `gorm:"type:varchar(36);default:''" json:"branch_id"`

	// IngredientID — insumo emparejado (nullable hasta confirmar el match;
	// *string + UUIDPtr middleware para no romper inserts en Postgres).
	IngredientID *string `gorm:"type:uuid;index" json:"ingredient_uuid,omitempty"`
	RawName      string  `gorm:"not null" json:"raw_name"`

	Source       string  `gorm:"type:varchar(20);not null;index" json:"source"`
	SupplierID   *string `gorm:"type:uuid" json:"supplier_uuid,omitempty"`
	SupplierName string  `gorm:"default:''" json:"supplier_name"`

	UnitPrice float64 `gorm:"not null" json:"unit_price"`
	// PackUnit/PackQty: cómo se vende (ej bulto 50 kg). PricePerBaseUnit
	// normaliza a la unidad del Ingredient (g/kg/ml/l/unidad) para comparar.
	PackUnit         string  `gorm:"type:varchar(16);default:''" json:"pack_unit"`
	PackQty          float64 `gorm:"default:0" json:"pack_qty"`
	PricePerBaseUnit float64 `gorm:"default:0" json:"price_per_base_unit"`
	Currency         string  `gorm:"type:varchar(8);default:'COP'" json:"currency"`

	Confidence float64   `gorm:"default:0.5" json:"confidence"`
	CapturedAt time.Time `gorm:"index" json:"captured_at"`
	SourceRef  string    `gorm:"default:''" json:"source_ref"` // id de factura / url / cadena
}
