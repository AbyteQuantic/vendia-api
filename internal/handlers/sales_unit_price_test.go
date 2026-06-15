// Spec: specs/049-venta-respeta-precio-efectivo/spec.md
package handlers

import "testing"

func fptr(v float64) *float64 { return &v }

func TestResolveLineUnitPrice(t *testing.T) {
	cases := []struct {
		name    string
		retail  float64
		reqUnit *float64
		want    float64
	}{
		{"sin unit_price → retail (retrocompatible)", 28500, nil, 28500},
		{"unit_price de tier → usa el tier", 28500, fptr(25000), 25000},
		{"unit_price 0 → retail (no acepta cero)", 28500, fptr(0), 28500},
		{"unit_price negativo → retail (guard)", 28500, fptr(-5), 28500},
		{"unit_price > retail (mayorista al revés) se respeta", 1000, fptr(1200), 1200},
	}
	for _, c := range cases {
		if got := resolveLineUnitPrice(c.retail, c.reqUnit); got != c.want {
			t.Errorf("%s: resolveLineUnitPrice(%v, %v) = %v, want %v",
				c.name, c.retail, c.reqUnit, got, c.want)
		}
	}
}

// AC-03 (cálculo del total): con tier, el total = Σ(unit_price × qty), no retail.
func TestSaleTotalUsesTierUnitPrice(t *testing.T) {
	// 3 sacos de cemento: retail 28.500, tier_1 25.000.
	retail := 28500.0
	tier := fptr(25000.0)
	qty := 3

	total := 0.0
	total += resolveLineUnitPrice(retail, tier) * float64(qty)

	if total != 75000 { // 3 × 25.000, NO 85.500 (retail)
		t.Fatalf("total con tier = %v, want 75000", total)
	}
}
