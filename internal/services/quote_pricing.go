// Spec: specs/031-cotizaciones/spec.md
package services

// QuotePriceLine is the pricing-relevant projection of a quote item.
// The handler builds these from the request payload (free lines and
// product lines alike) before persisting, so totals are computed once,
// server-side, from a single source of truth (Constitución Art. VII —
// dinero exacto).
type QuotePriceLine struct {
	Quantity  float64
	UnitPrice float64
	// Discount is the absolute per-line discount in COP.
	Discount float64
}

// QuoteTotals is the result of ComputeQuoteTotals: the per-line
// subtotals (index-aligned with the input lines) plus the rolled-up
// subtotal, tax, and grand total.
type QuoteTotals struct {
	LineSubtotals []float64
	Subtotal      float64
	TaxAmount     float64
	Total         float64
}

// ComputeQuoteTotals derives every monetary field of a quote from its
// lines, a global discount, and a tax rate (Spec F031 §4):
//
//   - lineSubtotal = (Quantity * UnitPrice) - Discount, clamped at >= 0;
//   - Subtotal     = sum of line subtotals (BEFORE the global discount);
//   - taxableBase  = max(Subtotal - discountTotal, 0);
//   - TaxAmount    = taxableBase * taxRate;
//   - Total        = taxableBase + TaxAmount.
//
// taxRate is a decimal fraction (0.19 = 19%), mirroring Tenant.VATRate.
// All amounts are in COP — multimoneda is out of scope (spec §6).
func ComputeQuoteTotals(lines []QuotePriceLine, discountTotal, taxRate float64) QuoteTotals {
	totals := QuoteTotals{}
	if len(lines) > 0 {
		totals.LineSubtotals = make([]float64, len(lines))
	}

	for i, line := range lines {
		sub := line.Quantity*line.UnitPrice - line.Discount
		if sub < 0 {
			sub = 0
		}
		totals.LineSubtotals[i] = sub
		totals.Subtotal += sub
	}

	taxableBase := totals.Subtotal - discountTotal
	if taxableBase < 0 {
		taxableBase = 0
	}

	totals.TaxAmount = taxableBase * taxRate
	totals.Total = taxableBase + totals.TaxAmount
	return totals
}
