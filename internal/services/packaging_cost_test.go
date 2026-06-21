// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import "testing"

func TestComputePackagingCost(t *testing.T) {
	cases := []struct {
		name                          string
		shortfall, packQty, packPrice float64
		wantPacks                     int
		wantCost, wantLeftover        float64
		wantKnown                     bool
	}{
		{"crema 2ml de bolsa 1L", 2, 1000, 4200, 1, 4200, 998, true},
		{"justo un empaque", 1000, 1000, 4200, 1, 4200, 0, true},
		{"dos empaques", 1500, 1000, 4200, 2, 8400, 500, true},
		{"un poquito mas de un empaque", 1001, 1000, 4200, 2, 8400, 999, true},
		{"sin empaque conocido (packQty 0)", 2, 0, 4200, 0, 0, 0, false},
		{"sin precio", 2, 1000, 0, 0, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ComputePackagingCost(c.shortfall, c.packQty, c.packPrice)
			if got.PackagingKnown != c.wantKnown {
				t.Fatalf("PackagingKnown = %v, want %v", got.PackagingKnown, c.wantKnown)
			}
			if !c.wantKnown {
				return
			}
			if got.Packs != c.wantPacks {
				t.Errorf("Packs = %d, want %d", got.Packs, c.wantPacks)
			}
			if got.Cost != c.wantCost {
				t.Errorf("Cost = %v, want %v", got.Cost, c.wantCost)
			}
			if got.Leftover != c.wantLeftover {
				t.Errorf("Leftover = %v, want %v", got.Leftover, c.wantLeftover)
			}
			// INVARIANTE: el costo nunca es menor a un empaque.
			if got.Cost < c.packPrice {
				t.Errorf("INVARIANTE rota: Cost %v < precioEmpaque %v", got.Cost, c.packPrice)
			}
		})
	}
}
