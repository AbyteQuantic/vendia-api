// Spec: specs/075-proveedores-b2b/spec.md
package models

// Opciones de entrega (el fundador: "por sus medios" + transportistas a futuro).
const (
	DeliveryProveedorEntrega = "proveedor_entrega" // el proveedor lleva
	DeliveryTiendaRecoge     = "tienda_recoge"     // la tienda recoge
	DeliveryPorAcordar       = "por_acordar"       // lo hablan por WhatsApp
)

// Estados del pedido a proveedor.
const (
	SupplierOrderNuevo      = "nuevo"
	SupplierOrderConfirmado = "confirmado"
	SupplierOrderEntregado  = "entregado"
	SupplierOrderCancelado  = "cancelado"
)

// SupplierOrder — pedido CROSS-TENANT de una tienda (buyer) a un proveedor
// (supplier). VendIA SOLO CONECTA: registra la intención (para medir GMV
// intermediado) y el cierre real se hace por WhatsApp. No procesa el pago.
type SupplierOrder struct {
	BaseModel

	SupplierTenantID string `gorm:"type:uuid;index;not null" json:"supplier_tenant_id"`
	BuyerTenantID    string `gorm:"type:uuid;index;not null" json:"buyer_tenant_id"`

	// Snapshot del comprador (la tienda) — para que el proveedor sepa quién pide.
	BuyerName  string `gorm:"not null" json:"buyer_name"`
	BuyerPhone string `gorm:"default:''" json:"buyer_phone"`

	Items          string  `gorm:"type:jsonb;default:'[]'" json:"items"` // [{product_id,name,quantity,price}]
	TotalAmount    float64 `gorm:"default:0" json:"total_amount"`
	DeliveryChoice string  `gorm:"default:'por_acordar'" json:"delivery_choice"`
	Status         string  `gorm:"default:'nuevo'" json:"status"`
	Notes          string  `gorm:"default:''" json:"notes"`
}
