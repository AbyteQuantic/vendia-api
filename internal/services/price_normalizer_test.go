// Spec: specs/027-importador-inventario/spec.md
package services

import (
	"math"
	"testing"
)

// TestNormalizePriceCOP covers all formats defined in FR-10 and the edge cases
// from spec §9 (formato europeo, vacío, cero, negativo).
func TestNormalizePriceCOP(t *testing.T) {
	type tc struct {
		input   string
		want    float64 // only checked when wantErr is false
		wantErr bool
	}

	cases := []tc{
		// ── Basic integers ────────────────────────────────────────────
		{"1500", 1500.0, false},
		{"1500.50", 1500.5, false},

		// ── COP currency prefix ───────────────────────────────────────
		{"$1500", 1500.0, false},
		{"$ 1500", 1500.0, false},

		// ── Dot as thousands separator (colombiano) ───────────────────
		{"1.500", 1500.0, false},
		{"$ 1.500", 1500.0, false},
		{"1.500.000", 1500000.0, false},

		// ── Comma as thousands separator ──────────────────────────────
		{"1,500", 1500.0, false},
		{"1,500,000", 1500000.0, false},

		// ── Mixed: dot as thousands AND comma as decimal (europeo) ────
		// "1.500,50" → dot=miles, comma=decimal → 1500.50
		{"1.500,50", 1500.5, false},
		{"1.500,00", 1500.0, false},

		// ── Mixed: comma as thousands AND dot as decimal (US) ─────────
		// "1,500.00" → comma=miles, dot=decimal → 1500.00
		{"1,500.00", 1500.0, false},
		{"1,500.50", 1500.5, false},

		// ── Decimal only (single dot or comma) ────────────────────────
		{"1.5", 1.5, false},
		{"1,5", 1.5, false},

		// ── With spaces inside ────────────────────────────────────────
		{"$ 1.500", 1500.0, false},
		{" 2500 ", 2500.0, false},

		// ── Error cases ───────────────────────────────────────────────
		{"abc", 0, true},
		{"", 0, true},
		{"0", 0, true},
		{"0.00", 0, true},
		{"-100", 0, true},
		{"-1.500", 0, true},
		{"$0", 0, true},
	}

	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got, err := NormalizePriceCOP(c.input)
			if c.wantErr {
				if err == nil {
					t.Errorf("NormalizePriceCOP(%q) = %v, want error", c.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("NormalizePriceCOP(%q) returned unexpected error: %v", c.input, err)
				return
			}
			if math.Abs(got-c.want) > 0.001 {
				t.Errorf("NormalizePriceCOP(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}
