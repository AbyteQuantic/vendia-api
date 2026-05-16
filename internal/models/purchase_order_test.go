// Spec: specs/002-ordenes-compra/spec.md
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// T-01 — RED: PurchaseOrder / PurchaseOrderItem are the two new
// entities of Feature 002. Spec §8 / Plan §3: fields, status enum,
// and the insumo-XOR-producto invariant on each item.

func TestPurchaseOrder_Fields(t *testing.T) {
	po := PurchaseOrder{
		TenantID:   "11111111-1111-1111-1111-111111111111",
		SupplierID: "22222222-2222-2222-2222-222222222222",
		Status:     PurchaseOrderDraft,
		Total:      48000,
		Notes:      "Pedido semanal",
	}
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", po.SupplierID)
	assert.Equal(t, PurchaseOrderDraft, po.Status)
	assert.Equal(t, float64(48000), po.Total)
	assert.Equal(t, "Pedido semanal", po.Notes)
}

// FR-03 — the lifecycle enum is the fixed set {borrador, enviada,
// recibida, cancelada}.
func TestIsValidPurchaseOrderStatus(t *testing.T) {
	valid := []string{"borrador", "enviada", "recibida", "cancelada"}
	for _, s := range valid {
		assert.True(t, IsValidPurchaseOrderStatus(s), "expected %q to be valid", s)
	}
	invalid := []string{"", "draft", "BORRADOR", "pendiente", "enviado"}
	for _, s := range invalid {
		assert.False(t, IsValidPurchaseOrderStatus(s), "expected %q to be invalid", s)
	}
}

// FR-03 / §7 — only valid transitions are allowed. A received or
// cancelled PO is terminal.
func TestPurchaseOrder_CanTransitionTo(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{"draft → sent", PurchaseOrderDraft, PurchaseOrderSent, true},
		{"draft → received (D3 direct receive)", PurchaseOrderDraft, PurchaseOrderReceived, true},
		{"draft → cancelled", PurchaseOrderDraft, PurchaseOrderCancelled, true},
		{"sent → received", PurchaseOrderSent, PurchaseOrderReceived, true},
		{"sent → cancelled", PurchaseOrderSent, PurchaseOrderCancelled, true},
		{"received is terminal", PurchaseOrderReceived, PurchaseOrderSent, false},
		{"received → cancelled rejected", PurchaseOrderReceived, PurchaseOrderCancelled, false},
		{"cancelled is terminal", PurchaseOrderCancelled, PurchaseOrderReceived, false},
		{"sent → draft not allowed", PurchaseOrderSent, PurchaseOrderDraft, false},
		{"draft → draft no-op rejected", PurchaseOrderDraft, PurchaseOrderDraft, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			po := PurchaseOrder{Status: tc.from}
			assert.Equal(t, tc.want, po.CanTransitionTo(tc.to))
		})
	}
}

// §7 — a received or cancelled PO is terminal.
func TestPurchaseOrder_IsTerminal(t *testing.T) {
	assert.False(t, PurchaseOrder{Status: PurchaseOrderDraft}.IsTerminal())
	assert.False(t, PurchaseOrder{Status: PurchaseOrderSent}.IsTerminal())
	assert.True(t, PurchaseOrder{Status: PurchaseOrderReceived}.IsTerminal())
	assert.True(t, PurchaseOrder{Status: PurchaseOrderCancelled}.IsTerminal())
}

// D1 / §7 — a PurchaseOrderItem references exactly one insumo XOR one
// product: never both, never neither.
func TestPurchaseOrderItem_IsValidReference(t *testing.T) {
	ingredientID := "33333333-3333-3333-3333-333333333333"
	productID := "44444444-4444-4444-4444-444444444444"

	cases := []struct {
		name       string
		ingredient *string
		product    *string
		want       bool
	}{
		{"only ingredient", &ingredientID, nil, true},
		{"only product", nil, &productID, true},
		{"both set — invalid", &ingredientID, &productID, false},
		{"neither set — invalid", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := PurchaseOrderItem{
				IngredientID: tc.ingredient,
				ProductID:    tc.product,
			}
			assert.Equal(t, tc.want, item.IsValidReference())
		})
	}
}

// D1 — an empty-string FK pointer counts as "not set" so a string ""
// never sneaks past the XOR invariant.
func TestPurchaseOrderItem_IsValidReference_TreatsEmptyStringAsUnset(t *testing.T) {
	empty := ""
	productID := "44444444-4444-4444-4444-444444444444"
	item := PurchaseOrderItem{IngredientID: &empty, ProductID: &productID}
	assert.True(t, item.IsValidReference(), "an empty IngredientID must not count as a second reference")

	bothEmpty := PurchaseOrderItem{IngredientID: &empty, ProductID: &empty}
	assert.False(t, bothEmpty.IsValidReference(), "two empty pointers reference nothing")
}

// FR-02 — an item carries quantity and unit cost; both must be > 0.
func TestPurchaseOrderItem_HasValidAmounts(t *testing.T) {
	cases := []struct {
		name     string
		quantity float64
		unitCost float64
		want     bool
	}{
		{"positive amounts", 10, 2900, true},
		{"zero quantity", 0, 2900, false},
		{"zero cost", 10, 0, false},
		{"negative quantity", -1, 2900, false},
		{"negative cost", 10, -5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := PurchaseOrderItem{Quantity: tc.quantity, UnitCost: tc.unitCost}
			assert.Equal(t, tc.want, item.HasValidAmounts())
		})
	}
}

// FR-01 — the line total of an item is quantity * unit cost.
func TestPurchaseOrderItem_LineTotal(t *testing.T) {
	item := PurchaseOrderItem{Quantity: 10, UnitCost: 2900}
	assert.Equal(t, float64(29000), item.LineTotal())
}

// FR-01 — the PO total is the sum of its item line totals.
func TestPurchaseOrder_ComputeTotal(t *testing.T) {
	po := PurchaseOrder{
		Items: []PurchaseOrderItem{
			{Quantity: 10, UnitCost: 2900}, // 29000
			{Quantity: 2, UnitCost: 12000}, // 24000
		},
	}
	assert.Equal(t, float64(53000), po.ComputeTotal())
}

// MovementPurchaseReceipt is the new kardex movement type for a PO
// receipt (Plan §3, additive — Art. X).
func TestMovementPurchaseReceipt_Constant(t *testing.T) {
	assert.Equal(t, MovementType("purchase_receipt"), MovementPurchaseReceipt)
}
