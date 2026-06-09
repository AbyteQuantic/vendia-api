// Spec: specs/042-modulo-eventos/spec.md (§12 D1)
package services

import "fmt"

// EventReminderMessage builds the WhatsApp text reminding an attendee that
// their event is coming up. The organizer launches this via a wa.me deep-link
// (assisted send, patrón F033) — attendees have no push subscription (D1).
func (s *WhatsAppService) EventReminderMessage(attendeeName, businessName, eventTitle, whenStr string) string {
	return fmt.Sprintf(
		"Hola %s. Le recordamos que el evento \"%s\" de %s será %s. ¡Le esperamos!",
		attendeeName, eventTitle, businessName, whenStr)
}

// EventQuotaReminderMessage builds the WhatsApp text reminding an attendee of a
// pending event installment, including the amount and the due date.
func (s *WhatsAppService) EventQuotaReminderMessage(attendeeName, businessName, eventTitle string, amount int64, dueDateStr string) string {
	return fmt.Sprintf(
		"Hola %s. Le recordamos su cuota de $%s del evento \"%s\" en %s, con fecha %s. ¡Gracias!",
		attendeeName, formatCOP(float64(amount)), eventTitle, businessName, dueDateStr)
}
