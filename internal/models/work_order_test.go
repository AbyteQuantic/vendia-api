// Spec: specs/003-trabajos-muebles/spec.md
package models

import "testing"

// AC-01 — total of a work order is the sum of every item's line total:
// a material line (qty * unit_price) plus a labour line (unit_price).
func TestWorkOrder_ComputeTotal_SumsItems(t *testing.T) {
	wo := WorkOrder{
		Items: []WorkOrderItem{
			{Kind: WorkOrderItemMaterial, Quantity: 2, UnitPrice: 20000}, // 40000
			{Kind: WorkOrderItemLabor, Quantity: 1, UnitPrice: 50000},    // 50000
		},
	}
	if got := wo.ComputeTotal(); got != 90000 {
		t.Fatalf("ComputeTotal() = %v, want 90000", got)
	}
}

// A work order with no items totals 0 (§9 — caso borde).
func TestWorkOrder_ComputeTotal_EmptyIsZero(t *testing.T) {
	wo := WorkOrder{}
	if got := wo.ComputeTotal(); got != 0 {
		t.Fatalf("ComputeTotal() = %v, want 0", got)
	}
}

// AC-02 — Paid sums the payments; Balance is total minus paid.
func TestWorkOrder_PaidAndBalance(t *testing.T) {
	wo := WorkOrder{
		Total: 90000,
		Payments: []WorkOrderPayment{
			{Amount: 40000},
		},
	}
	if got := wo.Paid(); got != 40000 {
		t.Fatalf("Paid() = %v, want 40000", got)
	}
	if got := wo.Balance(); got != 50000 {
		t.Fatalf("Balance() = %v, want 50000", got)
	}
}

// Balance never goes below zero even if payments somehow exceed total.
func TestWorkOrder_Balance_NeverNegative(t *testing.T) {
	wo := WorkOrder{
		Total:    50000,
		Payments: []WorkOrderPayment{{Amount: 60000}},
	}
	if got := wo.Balance(); got != 0 {
		t.Fatalf("Balance() = %v, want 0 (clamped)", got)
	}
}

// AC-05 — the lifecycle machine only allows valid transitions.
func TestWorkOrder_CanTransitionTo(t *testing.T) {
	cases := []struct {
		from string
		to   string
		want bool
	}{
		{WorkOrderQuote, WorkOrderApproved, true},
		{WorkOrderQuote, WorkOrderCancelled, true},
		{WorkOrderQuote, WorkOrderDelivered, false}, // AC-05 — skip ahead rejected
		{WorkOrderQuote, WorkOrderInProgress, false},
		{WorkOrderApproved, WorkOrderInProgress, true},
		{WorkOrderApproved, WorkOrderCancelled, true},
		{WorkOrderApproved, WorkOrderQuote, false}, // no going back
		{WorkOrderInProgress, WorkOrderCompleted, true},
		{WorkOrderInProgress, WorkOrderCancelled, true},
		{WorkOrderCompleted, WorkOrderDelivered, true},
		{WorkOrderCompleted, WorkOrderInProgress, false},
		{WorkOrderDelivered, WorkOrderCancelled, false}, // terminal
		{WorkOrderCancelled, WorkOrderQuote, false},     // terminal
		{WorkOrderQuote, WorkOrderQuote, false},         // self-transition invalid
	}
	for _, c := range cases {
		wo := WorkOrder{Status: c.from}
		if got := wo.CanTransitionTo(c.to); got != c.want {
			t.Errorf("CanTransitionTo(%s→%s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// entregada and cancelada are terminal — no further transitions (§7).
func TestWorkOrder_IsTerminal(t *testing.T) {
	for _, s := range []string{WorkOrderDelivered, WorkOrderCancelled} {
		if !(WorkOrder{Status: s}).IsTerminal() {
			t.Errorf("status %s must be terminal", s)
		}
	}
	for _, s := range []string{WorkOrderQuote, WorkOrderApproved, WorkOrderInProgress, WorkOrderCompleted} {
		if (WorkOrder{Status: s}).IsTerminal() {
			t.Errorf("status %s must NOT be terminal", s)
		}
	}
}

// FR-07 — items are editable only while the order is cotizacion or aprobada.
func TestWorkOrder_ItemsEditable(t *testing.T) {
	for _, s := range []string{WorkOrderQuote, WorkOrderApproved} {
		if !(WorkOrder{Status: s}).ItemsEditable() {
			t.Errorf("status %s must allow item editing", s)
		}
	}
	for _, s := range []string{WorkOrderInProgress, WorkOrderCompleted, WorkOrderDelivered, WorkOrderCancelled} {
		if (WorkOrder{Status: s}).ItemsEditable() {
			t.Errorf("status %s must freeze items (AC-07)", s)
		}
	}
}

// IsValidWorkOrderStatus / IsValidWorkOrderType guard the enums.
func TestWorkOrder_EnumValidation(t *testing.T) {
	for _, s := range []string{WorkOrderQuote, WorkOrderApproved, WorkOrderInProgress, WorkOrderCompleted, WorkOrderDelivered, WorkOrderCancelled} {
		if !IsValidWorkOrderStatus(s) {
			t.Errorf("%s must be a valid status", s)
		}
	}
	if IsValidWorkOrderStatus("bogus") {
		t.Error("bogus must not be a valid status")
	}
	for _, ty := range []string{WorkOrderTypeFabrication, WorkOrderTypeRepair} {
		if !IsValidWorkOrderType(ty) {
			t.Errorf("%s must be a valid type", ty)
		}
	}
	if IsValidWorkOrderType("bogus") {
		t.Error("bogus must not be a valid type")
	}
}

// Invariant — a material item references an insumo XOR a product.
func TestWorkOrderItem_MaterialReference(t *testing.T) {
	ing := "i1"
	prod := "p1"
	cases := []struct {
		name string
		item WorkOrderItem
		want bool
	}{
		{"material with insumo only", WorkOrderItem{Kind: WorkOrderItemMaterial, IngredientID: &ing}, true},
		{"material with product only", WorkOrderItem{Kind: WorkOrderItemMaterial, ProductID: &prod}, true},
		{"material with both", WorkOrderItem{Kind: WorkOrderItemMaterial, IngredientID: &ing, ProductID: &prod}, false},
		{"material with neither", WorkOrderItem{Kind: WorkOrderItemMaterial}, false},
	}
	for _, c := range cases {
		if got := c.item.IsValidReference(); got != c.want {
			t.Errorf("%s: IsValidReference() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Invariant — a mano_obra item must NOT reference inventory.
func TestWorkOrderItem_LaborReference(t *testing.T) {
	ing := "i1"
	if (WorkOrderItem{Kind: WorkOrderItemLabor}).IsValidReference() != true {
		t.Error("labour without a reference must be valid")
	}
	if (WorkOrderItem{Kind: WorkOrderItemLabor, IngredientID: &ing}).IsValidReference() {
		t.Error("labour referencing inventory must be invalid")
	}
}

// HasValidAmounts — quantity and unit price must be strictly positive.
func TestWorkOrderItem_HasValidAmounts(t *testing.T) {
	cases := []struct {
		item WorkOrderItem
		want bool
	}{
		{WorkOrderItem{Quantity: 2, UnitPrice: 20000}, true},
		{WorkOrderItem{Quantity: 0, UnitPrice: 20000}, false},
		{WorkOrderItem{Quantity: 2, UnitPrice: 0}, false},
		{WorkOrderItem{Quantity: -1, UnitPrice: 20000}, false},
	}
	for _, c := range cases {
		if got := c.item.HasValidAmounts(); got != c.want {
			t.Errorf("HasValidAmounts(%+v) = %v, want %v", c.item, got, c.want)
		}
	}
}

// LineTotal — quantity * unit price.
func TestWorkOrderItem_LineTotal(t *testing.T) {
	it := WorkOrderItem{Quantity: 3, UnitPrice: 15000}
	if got := it.LineTotal(); got != 45000 {
		t.Fatalf("LineTotal() = %v, want 45000", got)
	}
}

// IsValidKind guards the item-kind enum.
func TestWorkOrderItem_IsValidKind(t *testing.T) {
	if !IsValidWorkOrderItemKind(WorkOrderItemMaterial) || !IsValidWorkOrderItemKind(WorkOrderItemLabor) {
		t.Error("material and mano_obra must be valid kinds")
	}
	if IsValidWorkOrderItemKind("bogus") {
		t.Error("bogus must not be a valid kind")
	}
}
