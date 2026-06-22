// Spec: specs/077-compra-inteligente-insumos/spec.md
package models

import "time"

// Tipo de asignado de un mandado.
const (
	AssigneeSupplier        = "supplier"         // proveedor propio
	AssigneeWhatsAppContact = "whatsapp_contact" // un número de WhatsApp suelto
	AssigneeEmployee        = "employee"         // un empleado registrado
	AssigneeSelf            = "self"             // el mismo tenant lo compra
)

// Estado de un mandado.
const (
	ErrandPendiente = "pendiente"
	ErrandEnviado   = "enviado"
	ErrandComprado  = "comprado"
	ErrandCancelado = "cancelado"
)

// PurchaseErrand — un MANDADO de compra (Spec 077): una lista de insumos a
// comprar, asignada a un proveedor / contacto / empleado, con su estado. Permite
// "enviar por partes" y sugerir "reenviar lo de hoy". VendIA solo conecta.
type PurchaseErrand struct {
	BaseModel

	TenantID string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	BranchID string `gorm:"type:varchar(36);default:''" json:"branch_id"`

	Title         string  `gorm:"default:''" json:"title"`
	AssigneeType  string  `gorm:"type:varchar(20);default:'self'" json:"assignee_type"`
	AssigneeID    *string `gorm:"type:uuid" json:"assignee_uuid,omitempty"` // FK Supplier/Employee
	AssigneeName  string  `gorm:"default:''" json:"assignee_name"`
	AssigneePhone string  `gorm:"default:''" json:"assignee_phone"`

	Status         string  `gorm:"type:varchar(16);default:'pendiente';index" json:"status"`
	TotalEstimated float64 `gorm:"default:0" json:"total_estimated"`
	Note           string  `gorm:"default:''" json:"note"`

	Lines    []PurchaseErrandLine `gorm:"foreignKey:ErrandID;references:ID" json:"lines"`
	ClosedAt *time.Time           `json:"closed_at,omitempty"`
}

// PurchaseErrandLine — un insumo dentro de un mandado.
type PurchaseErrandLine struct {
	BaseModel

	ErrandID     string  `gorm:"type:uuid;index;not null" json:"errand_id"`
	IngredientID *string `gorm:"type:uuid" json:"ingredient_uuid,omitempty"`
	// LineKind discrimina QUÉ se compra: 'ingredient' (insumo, default — preserva los
	// mandados actuales) o 'product' (producto de tienda). varchar(16), NO uuid
	// default '' (lección AutoMigrate Spec 066). Spec 078 B1.
	LineKind  string  `gorm:"type:varchar(16);default:'ingredient'" json:"line_kind"`
	ProductID *string `gorm:"type:uuid" json:"product_uuid,omitempty"` // espejo cuando LineKind='product'
	Name      string  `gorm:"not null" json:"name"`
	Unit      string  `gorm:"default:''" json:"unit"`
	Qty       float64 `gorm:"default:0" json:"qty"`

	EstimatedUnitPrice float64 `gorm:"default:0" json:"estimated_unit_price"`
	EstimatedCost      float64 `gorm:"default:0" json:"estimated_cost"`
	PriceSource        string  `gorm:"default:''" json:"price_source"`
	IsEstimate         bool    `gorm:"default:true" json:"is_estimate"`
	// ReceivedQty/Fulfilled — compra PARCIAL (Spec 078 B3): cuánto se ingresó de
	// verdad y si la línea quedó completa. Default 0/false = comportamiento actual.
	ReceivedQty float64 `gorm:"default:0" json:"received_qty"`
	Fulfilled   bool    `gorm:"default:false" json:"fulfilled"`
}
