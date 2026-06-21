// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import "testing"

func TestToBaseQty(t *testing.T) {
	cases := []struct {
		qty      float64
		unit     string
		wantQty  float64
		wantDim  string
	}{
		{2, "kg", 2000, "mass"},
		{500, "g", 500, "mass"},
		{1, "l", 1000, "vol"},
		{900, "ml", 900, "vol"},
		{3, "unidad", 3, "count"},
		{5, "rareza", 5, "other"},
	}
	for _, c := range cases {
		q, d := ToBaseQty(c.qty, c.unit)
		if q != c.wantQty || d != c.wantDim {
			t.Errorf("ToBaseQty(%v,%q)=%v/%q want %v/%q", c.qty, c.unit, q, d, c.wantQty, c.wantDim)
		}
	}
}
