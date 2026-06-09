// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"context"
	"fmt"
	"strings"
)

// themeHint turns the organizer's free-text description into a soft styling
// hint for the image model, so the generated piece matches the event's tema
// (colores, motivos) instead of a generic template. Empty description → no
// extra line, keeping the prompt stable for events created before this field.
func themeHint(description string) string {
	d := strings.TrimSpace(description)
	if d == "" {
		return ""
	}
	// Cap the length so a long description can't dominate the prompt.
	const maxLen = 400
	if len(d) > maxLen {
		d = d[:maxLen]
	}
	return fmt.Sprintf("\n\nContexto del evento (úsalo para inspirar colores, íconos y motivos acordes al tema, sin escribir este texto en la pieza):\n%s", d)
}

// buildEventBadgePrompt composes the prompt for an event badge (escarapela).
// It names the event, the organizer and the attendee, and explicitly reserves
// a clear area for the validation QR (scanned at check-in/out — decision #3).
// The design is professional and print-friendly; text in Spanish (Art. V).
// description (optional) themes the piece to the event's subject.
func buildEventBadgePrompt(eventTitle, businessName, attendeeName, description string) string {
	return fmt.Sprintf(`Diseña una ESCARAPELA (credencial) vertical, moderna y profesional para un evento.

Datos a mostrar de forma legible:
- Nombre del asistente: "%s" (grande y destacado, centrado)
- Evento: "%s"
- Organizador: "%s"

Requisitos de diseño:
- Estilo limpio y elegante, apto para impresión y para verse en pantalla de celular.
- Tipografía clara y de alto contraste; nada de texto decorativo ilegible.
- Reserva un recuadro blanco bien visible en la parte inferior para colocar un CÓDIGO QR de validación (no dibujes el QR, solo deja el espacio rotulado "QR").
- Paleta sobria con un color de acento; bordes redondeados.
- Todo el texto en español.%s`, attendeeName, eventTitle, businessName, themeHint(description))
}

// buildEventCertificatePrompt composes the prompt for a participation
// certificate. Horizontal, formal, with space for the attendee's name.
// description (optional) themes the piece to the event's subject.
func buildEventCertificatePrompt(eventTitle, businessName, attendeeName, description string) string {
	return fmt.Sprintf(`Diseña un CERTIFICADO de participación horizontal, formal y elegante.

Datos a mostrar:
- Título grande: "Certificado de participación"
- Nombre del participante: "%s" (destacado, en el centro)
- Evento: "%s"
- Otorgado por: "%s"

Requisitos de diseño:
- Marco ornamental sobrio, fondo claro, tipografía serif legible.
- Deja un espacio inferior derecho para un código QR de verificación rotulado "QR".
- Todo el texto en español; aspecto digno de imprimir.%s`, attendeeName, eventTitle, businessName, themeHint(description))
}

// GenerateEventBadge renders an escarapela design for an event via Gemini and
// returns raw image bytes the caller uploads to storage. It reuses the
// prompt→image path of GeneratePromoBanner (same generationConfig, temperature
// tuned for legible typography). NOTE: FinOps usage is currently logged under
// PROMO_BANNER; a dedicated EVENT_BADGE label (constant already defined) needs
// the image path to be parameterized by feature — tracked as a follow-up.
func (s *GeminiService) GenerateEventBadge(ctx context.Context, eventTitle, businessName, attendeeName, description string) ([]byte, error) {
	return s.GeneratePromoBanner(ctx, buildEventBadgePrompt(eventTitle, businessName, attendeeName, description), nil)
}

// GenerateEventCertificate renders a participation certificate design. Same
// FinOps-label caveat as GenerateEventBadge.
func (s *GeminiService) GenerateEventCertificate(ctx context.Context, eventTitle, businessName, attendeeName, description string) ([]byte, error) {
	return s.GeneratePromoBanner(ctx, buildEventCertificatePrompt(eventTitle, businessName, attendeeName, description), nil)
}
