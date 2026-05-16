// Spec: specs/003-trabajos-muebles/spec.md
package models

import "time"

// WorkOrder lifecycle states (Spec §2, FR-03). The taller owner sees
// these strings directly, so they are Spanish (Art. V). A delivered or
// cancelled work order is terminal.
const (
	WorkOrderQuote      = "cotizacion"
	WorkOrderApproved   = "aprobada"
	WorkOrderInProgress = "en_proceso"
	WorkOrderCompleted  = "terminada"
	WorkOrderDelivered  = "entregada"
	WorkOrderCancelled  = "cancelada"
)

// WorkOrder type — a job is either fabrication or repair (Spec §2).
const (
	WorkOrderTypeFabrication = "fabricacion"
	WorkOrderTypeRepair      = "reparacion"
)

// WorkOrderItem kinds — a line is either a material (insumo/producto) or
// labour (Spec FR-02).
const (
	WorkOrderItemMaterial = "material"
	WorkOrderItemLabor    = "mano_obra"
)

// validWorkOrderStatuses is the source of truth for status validation.
var validWorkOrderStatuses = map[string]bool{
	WorkOrderQuote:      true,
	WorkOrderApproved:   true,
	WorkOrderInProgress: true,
	WorkOrderCompleted:  true,
	WorkOrderDelivered:  true,
	WorkOrderCancelled:  true,
}

// IsValidWorkOrderStatus reports whether s is one of the six fixed
// lifecycle states.
func IsValidWorkOrderStatus(s string) bool {
	return validWorkOrderStatuses[s]
}

// validWorkOrderTypes is the source of truth for type validation.
var validWorkOrderTypes = map[string]bool{
	WorkOrderTypeFabrication: true,
	WorkOrderTypeRepair:      true,
}

// IsValidWorkOrderType reports whether s is fabricacion or reparacion.
func IsValidWorkOrderType(s string) bool {
	return validWorkOrderTypes[s]
}

// validWorkOrderItemKinds is the source of truth for kind validation.
var validWorkOrderItemKinds = map[string]bool{
	WorkOrderItemMaterial: true,
	WorkOrderItemLabor:    true,
}

// IsValidWorkOrderItemKind reports whether s is material or mano_obra.
func IsValidWorkOrderItemKind(s string) bool {
	return validWorkOrderItemKinds[s]
}

// validWorkOrderTransitions encodes the lifecycle machine (FR-03, §7):
// cotizacion → aprobada → en_proceso → terminada → entregada, with
// cancelada reachable from any non-terminal state. Delivered and
// cancelled are terminal — they map to nothing.
var validWorkOrderTransitions = map[string]map[string]bool{
	WorkOrderQuote: {
		WorkOrderApproved:  true,
		WorkOrderCancelled: true,
	},
	WorkOrderApproved: {
		WorkOrderInProgress: true,
		WorkOrderCancelled:  true,
	},
	WorkOrderInProgress: {
		WorkOrderCompleted: true,
		WorkOrderCancelled: true,
	},
	WorkOrderCompleted: {
		WorkOrderDelivered: true,
		WorkOrderCancelled: true,
	},
}

// WorkOrder (trabajo) is a furniture fabrication / repair job: a
// customer encargo with its quotation items, customer advances and a
// lifecycle from quote to delivery. Stock for its material items moves
// ONLY through a kardex movement when the job is completed (Spec §2,
// FR-05). Multi-tenant: every query filters by TenantID (Art. III).
type WorkOrder struct {
	BaseModel

	TenantID    string             `gorm:"type:uuid;not null;index" json:"tenant_id"`
	CustomerID  string             `gorm:"type:uuid;not null;index" json:"customer_id"`
	Type        string             `gorm:"type:varchar(16);not null;default:'fabricacion'" json:"type"`
	Status      string             `gorm:"type:varchar(16);not null;default:'cotizacion'" json:"status"`
	Description string             `gorm:"type:text" json:"description"`
	Total       float64            `gorm:"default:0" json:"total"`
	Notes       string             `gorm:"type:text" json:"notes,omitempty"`
	ApprovedAt  *time.Time         `json:"approved_at,omitempty"`
	CompletedAt *time.Time         `json:"completed_at,omitempty"`
	DeliveredAt *time.Time         `json:"delivered_at,omitempty"`
	Items       []WorkOrderItem    `gorm:"foreignKey:WorkOrderID" json:"items,omitempty"`
	Payments    []WorkOrderPayment `gorm:"foreignKey:WorkOrderID" json:"payments,omitempty"`
}

// CanTransitionTo reports whether the work order may move from its
// current status to next. A self-transition (same status) is never
// valid.
func (wo WorkOrder) CanTransitionTo(next string) bool {
	allowed, ok := validWorkOrderTransitions[wo.Status]
	if !ok {
		return false
	}
	return allowed[next]
}

// IsTerminal reports whether the work order is in a final state
// (entregada or cancelada) that admits no further transition (§7).
func (wo WorkOrder) IsTerminal() bool {
	return wo.Status == WorkOrderDelivered || wo.Status == WorkOrderCancelled
}

// ItemsEditable reports whether items can still be edited — only while
// the order is cotizacion or aprobada (FR-07, AC-07).
func (wo WorkOrder) ItemsEditable() bool {
	return wo.Status == WorkOrderQuote || wo.Status == WorkOrderApproved
}

// ComputeTotal sums the line totals of every item — the canonical work
// order total (FR-02, AC-01).
func (wo WorkOrder) ComputeTotal() float64 {
	var total float64
	for _, item := range wo.Items {
		total += item.LineTotal()
	}
	return total
}

// Paid sums every advance registered against the work order (FR-04).
func (wo WorkOrder) Paid() float64 {
	var paid float64
	for _, p := range wo.Payments {
		paid += p.Amount
	}
	return paid
}

// Balance is the outstanding amount: total minus paid, clamped at zero
// so an over-payment never reports a negative balance (FR-04, AC-02).
func (wo WorkOrder) Balance() float64 {
	balance := wo.Total - wo.Paid()
	if balance < 0 {
		return 0
	}
	return balance
}

// WorkOrderItem is one line of a WorkOrder. A `material` line references
// a raw-material insumo XOR a vendible product (Spec §7 invariant); a
// `mano_obra` line references no inventory — just a description and a
// price.
type WorkOrderItem struct {
	BaseModel

	WorkOrderID  string  `gorm:"type:uuid;not null;index" json:"work_order_id"`
	Kind         string  `gorm:"type:varchar(16);not null" json:"kind"`
	IngredientID *string `gorm:"type:uuid;index" json:"ingredient_id,omitempty"`
	ProductID    *string `gorm:"type:uuid;index" json:"product_id,omitempty"`
	Description  string  `gorm:"type:varchar(256)" json:"description"`
	Quantity     float64 `gorm:"not null;default:1" json:"quantity"`
	UnitPrice    float64 `gorm:"not null" json:"unit_price"`
}

// hasWORef reports whether a *string FK points at a real id — a nil
// pointer or an empty-string value both count as "not set".
func hasWORef(p *string) bool {
	return p != nil && *p != ""
}

// IsValidReference enforces the Spec §7 invariant: a `material` item
// references exactly one insumo XOR one product; a `mano_obra` item
// references no inventory at all.
func (item WorkOrderItem) IsValidReference() bool {
	if item.Kind == WorkOrderItemMaterial {
		return hasWORef(item.IngredientID) != hasWORef(item.ProductID)
	}
	// mano_obra — no inventory reference allowed.
	return !hasWORef(item.IngredientID) && !hasWORef(item.ProductID)
}

// HasValidAmounts reports whether quantity and unit price are both
// strictly positive (FR-02 — cantidad/precio ≤ 0 es rechazado).
func (item WorkOrderItem) HasValidAmounts() bool {
	return item.Quantity > 0 && item.UnitPrice > 0
}

// LineTotal is the cost of this line: quantity * unit price (FR-02).
func (item WorkOrderItem) LineTotal() float64 {
	return item.Quantity * item.UnitPrice
}

// WorkOrderPayment is one customer advance (anticipo) against a
// WorkOrder. Decoupled from OrderTicket on purpose (D3 — the audit
// flagged PartialPayment as bar/table-coupled). Multi-tenant: every
// query filters by TenantID (Art. III).
type WorkOrderPayment struct {
	BaseModel

	TenantID    string    `gorm:"type:uuid;not null;index" json:"tenant_id"`
	WorkOrderID string    `gorm:"type:uuid;not null;index" json:"work_order_id"`
	Amount      float64   `gorm:"not null" json:"amount"`
	Method      string    `gorm:"type:varchar(32)" json:"method"`
	PaidAt      time.Time `json:"paid_at"`
}
