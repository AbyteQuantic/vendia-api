// Spec: specs/002-ordenes-compra/spec.md
package models

import "time"

// PurchaseOrder lifecycle states (Spec §2, FR-03). The shopkeeper sees
// these strings directly, so they are Spanish (Art. V). A received or
// cancelled PO is terminal.
const (
	PurchaseOrderDraft     = "borrador"
	PurchaseOrderSent      = "enviada"
	PurchaseOrderReceived  = "recibida"
	PurchaseOrderCancelled = "cancelada"
)

// validPurchaseOrderStatuses is the source of truth for status
// validation. Kept private so callers go through the helper.
var validPurchaseOrderStatuses = map[string]bool{
	PurchaseOrderDraft:     true,
	PurchaseOrderSent:      true,
	PurchaseOrderReceived:  true,
	PurchaseOrderCancelled: true,
}

// IsValidPurchaseOrderStatus reports whether s is one of the four
// fixed lifecycle states.
func IsValidPurchaseOrderStatus(s string) bool {
	return validPurchaseOrderStatuses[s]
}

// validPurchaseOrderTransitions encodes the lifecycle machine (FR-03,
// §7, D3): borrador → enviada → recibida (+ cancelada). Receiving is
// allowed straight from borrador (D3 — compra sin envío formal).
// Received and cancelled are terminal — they map to nothing.
var validPurchaseOrderTransitions = map[string]map[string]bool{
	PurchaseOrderDraft: {
		PurchaseOrderSent:      true,
		PurchaseOrderReceived:  true,
		PurchaseOrderCancelled: true,
	},
	PurchaseOrderSent: {
		PurchaseOrderReceived:  true,
		PurchaseOrderCancelled: true,
	},
}

// PurchaseOrder (orden de compra) is a pedido a un proveedor with its
// items. Stock enters ONLY when it is received, and only via a kardex
// movement (Spec §7, D4). Multi-tenant: every query filters by
// TenantID (Art. III).
type PurchaseOrder struct {
	BaseModel

	TenantID   string              `gorm:"type:uuid;not null;index" json:"tenant_id"`
	SupplierID string              `gorm:"type:uuid;not null;index" json:"supplier_id"`
	Status     string              `gorm:"type:varchar(16);not null;default:'borrador'" json:"status"`
	Total      float64             `gorm:"default:0" json:"total"`
	Notes      string              `gorm:"type:text" json:"notes,omitempty"`
	SentAt     *time.Time          `json:"sent_at,omitempty"`
	ReceivedAt *time.Time          `json:"received_at,omitempty"`
	Items      []PurchaseOrderItem `gorm:"foreignKey:PurchaseOrderID" json:"items,omitempty"`
}

// CanTransitionTo reports whether the PO may move from its current
// status to next. A self-transition (same status) is never valid.
func (po PurchaseOrder) CanTransitionTo(next string) bool {
	allowed, ok := validPurchaseOrderTransitions[po.Status]
	if !ok {
		return false
	}
	return allowed[next]
}

// IsTerminal reports whether the PO is in a final state (recibida or
// cancelada) that admits no further transition (§7).
func (po PurchaseOrder) IsTerminal() bool {
	return po.Status == PurchaseOrderReceived || po.Status == PurchaseOrderCancelled
}

// ComputeTotal sums the line totals of every item — the canonical PO
// total (FR-01).
func (po PurchaseOrder) ComputeTotal() float64 {
	var total float64
	for _, item := range po.Items {
		total += item.LineTotal()
	}
	return total
}

// PurchaseOrderItem is one line of a PurchaseOrder. It references a
// raw-material insumo XOR a vendible product (D1, §7) and snapshots
// the name so a later rename or delete never rewrites history (FR-02).
type PurchaseOrderItem struct {
	BaseModel

	PurchaseOrderID string  `gorm:"type:uuid;not null;index" json:"purchase_order_id"`
	IngredientID    *string `gorm:"type:uuid;index" json:"ingredient_id,omitempty"`
	ProductID       *string `gorm:"type:uuid;index" json:"product_id,omitempty"`
	NameSnapshot    string  `gorm:"type:varchar(256);not null" json:"name_snapshot"`
	Quantity        float64 `gorm:"not null" json:"quantity"`
	UnitCost        float64 `gorm:"not null" json:"unit_cost"`
}

// hasRef reports whether a *string FK points at a real id — a nil
// pointer or an empty-string value both count as "not set" so a
// zero-value string never sneaks past the XOR invariant.
func hasRef(p *string) bool {
	return p != nil && *p != ""
}

// IsValidReference enforces D1: the item must reference exactly one
// insumo XOR one product — never both, never neither.
func (item PurchaseOrderItem) IsValidReference() bool {
	return hasRef(item.IngredientID) != hasRef(item.ProductID)
}

// HasValidAmounts reports whether quantity and unit cost are both
// strictly positive (FR-02, §9 — cantidad/costo ≤ 0 es rechazado).
func (item PurchaseOrderItem) HasValidAmounts() bool {
	return item.Quantity > 0 && item.UnitCost > 0
}

// LineTotal is the cost of this line: quantity * unit cost (FR-01).
func (item PurchaseOrderItem) LineTotal() float64 {
	return item.Quantity * item.UnitCost
}
