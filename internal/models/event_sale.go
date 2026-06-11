// Spec: specs/042-modulo-eventos/spec.md
package models

import "strings"

// eventSalePaymentMethod maps an inscription's recorded method to one of the
// Sale buckets so the event sale lands in the right column of the financial
// dashboard. Event money usually arrives as a transfer (Nequi / cobro digital).
func eventSalePaymentMethod(regMethod string) PaymentMethod {
	m := strings.ToLower(strings.TrimSpace(regMethod))
	switch {
	case strings.Contains(m, "efectivo") || strings.Contains(m, "cash"):
		return PaymentCash
	case strings.Contains(m, "fiado") || strings.Contains(m, "credit"):
		return PaymentCredit
	case strings.Contains(m, "tarjeta") || strings.Contains(m, "card") ||
		strings.Contains(m, "epayco") || strings.Contains(m, "pse"):
		return PaymentCard
	default:
		return PaymentTransfer
	}
}

// BuildEventSale constructs (without persisting) the ledger Sale that books a
// confirmed, paid inscription as a first-class sale of the business (Source =
// "EVENT"): revenue = event price, cost = event per-attendee cost. The single
// service line carries the event title for receipts/history. The caller
// persists it — the live path right after confirmation, or the bootstrap
// backfill (which also stamps CreatedAt to the registration date). Shared so
// both paths produce an identical row. Returns nil for a free event (no money).
func BuildEventSale(reg *EventRegistration, ev *Event, custName, custPhone string) *Sale {
	if ev.Price <= 0 {
		return nil
	}
	regID := reg.ID
	custID := reg.CustomerID
	return &Sale{
		TenantID:              reg.TenantID,
		Total:                 float64(ev.Price),
		CostAmount:            float64(ev.Cost),
		PaymentMethod:         eventSalePaymentMethod(reg.PaymentMethod),
		Source:                SaleSourceEvent,
		EventRegistrationID:   &regID,
		CustomerID:            &custID,
		CustomerNameSnapshot:  custName,
		CustomerPhoneSnapshot: custPhone,
		PaymentStatus:         "COMPLETED",
		Items: []SaleItem{{
			Name:              ev.Title,
			Price:             float64(ev.Price),
			Quantity:          1,
			Subtotal:          float64(ev.Price),
			IsService:         true,
			CustomDescription: "Inscripción: " + ev.Title,
			CustomUnitPrice:   float64(ev.Price),
		}},
	}
}
