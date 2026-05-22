// Spec: specs/031-cotizaciones/spec.md
package services_test

import (
	"testing"

	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
)

// TestComputeQuoteTotals exercises subtotal/tax/total math with and
// without a global discount and with and without a tax rate (Spec F031
// T-07). Per-line discounts are subtracted from each line's subtotal;
// the global discount applies on top of the summed line subtotals; tax
// is charged on the post-discount base.
func TestComputeQuoteTotals(t *testing.T) {
	tests := []struct {
		name          string
		lines         []services.QuotePriceLine
		discountTotal float64
		taxRate       float64
		wantLineSubs  []float64
		wantSubtotal  float64
		wantTax       float64
		wantTotal     float64
	}{
		{
			name: "sin descuento sin tax",
			lines: []services.QuotePriceLine{
				{Quantity: 2, UnitPrice: 1000, Discount: 0},
				{Quantity: 1, UnitPrice: 5000, Discount: 0},
			},
			discountTotal: 0,
			taxRate:       0,
			wantLineSubs:  []float64{2000, 5000},
			wantSubtotal:  7000,
			wantTax:       0,
			wantTotal:     7000,
		},
		{
			name: "con tax 19% sin descuento",
			lines: []services.QuotePriceLine{
				{Quantity: 1, UnitPrice: 10000, Discount: 0},
			},
			discountTotal: 0,
			taxRate:       0.19,
			wantLineSubs:  []float64{10000},
			wantSubtotal:  10000,
			wantTax:       1900,
			wantTotal:     11900,
		},
		{
			name: "con descuento global sin tax",
			lines: []services.QuotePriceLine{
				{Quantity: 4, UnitPrice: 2500, Discount: 0},
			},
			discountTotal: 1000,
			taxRate:       0,
			wantLineSubs:  []float64{10000},
			wantSubtotal:  10000,
			wantTax:       0,
			wantTotal:     9000,
		},
		{
			name: "descuento por linea + descuento global + tax",
			lines: []services.QuotePriceLine{
				{Quantity: 2, UnitPrice: 5000, Discount: 500}, // 10000-500 = 9500
				{Quantity: 1, UnitPrice: 3000, Discount: 0},   // 3000
			},
			discountTotal: 500,            // base = 12500 - 500 = 12000
			taxRate:       0.19,           // tax = 12000 * 0.19 = 2280
			wantLineSubs:  []float64{9500, 3000},
			wantSubtotal:  12500,
			wantTax:       2280,
			wantTotal:     14280, // 12000 + 2280
		},
		{
			name:          "sin lineas",
			lines:         nil,
			discountTotal: 0,
			taxRate:       0.19,
			wantLineSubs:  nil,
			wantSubtotal:  0,
			wantTax:       0,
			wantTotal:     0,
		},
		{
			name: "descuento global mayor al subtotal se clampa a 0",
			lines: []services.QuotePriceLine{
				{Quantity: 1, UnitPrice: 1000, Discount: 0},
			},
			discountTotal: 5000,
			taxRate:       0.19,
			wantLineSubs:  []float64{1000},
			wantSubtotal:  1000,
			wantTax:       0,
			wantTotal:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := services.ComputeQuoteTotals(tt.lines, tt.discountTotal, tt.taxRate)
			assert.InDelta(t, tt.wantSubtotal, got.Subtotal, 0.001, "subtotal")
			assert.InDelta(t, tt.wantTax, got.TaxAmount, 0.001, "tax")
			assert.InDelta(t, tt.wantTotal, got.Total, 0.001, "total")
			if assert.Len(t, got.LineSubtotals, len(tt.wantLineSubs), "line count") {
				for i, want := range tt.wantLineSubs {
					assert.InDelta(t, want, got.LineSubtotals[i], 0.001,
						"line %d subtotal", i)
				}
			}
		})
	}
}
