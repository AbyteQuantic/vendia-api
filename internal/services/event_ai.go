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

// PosterInput carries the event facts the marketing poster shows. Optional
// fields (DateText, empty PriceText→"Gratis") degrade gracefully so events
// created before a field existed still render.
type PosterInput struct {
	Title        string
	BusinessName string
	Type         string // raw type slug (curso/conferencia/hackaton/otro)
	TypeLabel    string // "Curso", "Conferencia", "Hackatón", "Evento"
	ModalityText string // "Presencial", "Virtual", "Híbrido"
	DateText     string // already formatted in es-CO, may be empty
	PriceText    string // "Gratis" or "$50.000", caller formats
	Description  string
	// Brief is the organizer's free-text creative direction ("muestra manos
	// decorando un pastel, colores pastel"). When present it drives the scene.
	Brief string
}

// posterScene picks the default photographic scene for the poster from the
// event type — people DOING the activity, so the model renders a real ad
// instead of text on a flat background. The organizer's brief overrides it.
func posterScene(eventType string) string {
	switch eventType {
	case "curso":
		return "un taller/clase real: un instructor enseñando y estudiantes participando con entusiasmo, manos en la actividad práctica del curso"
	case "conferencia":
		return "una conferencia profesional: un ponente carismático en un escenario moderno frente a una audiencia atenta, ambiente inspirador"
	case "hackaton":
		return "un hackatón lleno de energía: jóvenes desarrolladores colaborando con laptops y pantallas de código, ambiente de innovación y creatividad"
	default:
		return "personas reales disfrutando y participando activamente en el evento, ambiente cálido y atractivo"
	}
}

// buildEventPosterPrompt composes the prompt for an AFICHE PUBLICITARIO — the
// marketing piece shown in the public catalog (the WhatsApp link surfaces it).
// Unlike the badge/certificate it carries NO QR and no attendee name: it sells
// the event. The prompt pushes for AGENCY-QUALITY work: a real photographic/
// illustrated scene with people doing the activity, not text on a flat color —
// that was the failure mode the organizers reported.
func buildEventPosterPrompt(in PosterInput) string {
	// The scene: the organizer's brief wins; otherwise a sensible default per
	// type. Either way we DEMAND a rich illustrated/photographic composition.
	scene := strings.TrimSpace(in.Brief)
	sceneSource := "Sigue al pie de la letra estas indicaciones del organizador para la escena, el estilo y los elementos de la pieza"
	if scene == "" {
		scene = posterScene(in.Type)
		sceneSource = "Representa esta escena protagonista"
	}
	if len(scene) > 600 {
		scene = scene[:600]
	}

	price := in.PriceText
	if price == "" {
		price = "Gratis"
	}

	prompt := fmt.Sprintf(`Actúa como un DISEÑADOR PUBLICITARIO EXPERTO. Crea un AFICHE PUBLICITARIO PROFESIONAL, vertical (relación 4:5 o 9:16), con calidad de agencia de publicidad, para promocionar este evento. Debe verse espectacular en la pantalla de un celular y dar ganas de inscribirse.

LO MÁS IMPORTANTE — LA IMAGEN (no hagas SOLO texto sobre un fondo plano o un degradado):
- Crea una composición visual RICA con una FOTOGRAFÍA o ILUSTRACIÓN PROFESIONAL como protagonista.
- %s: %s.
- Incluye PERSONAS reales realizando la actividad, con expresiones y acciones creíbles; iluminación cinematográfica, profundidad, color graduado y composición dinámica.
- Calidad de portada de revista / campaña publicitaria real. NADA de plantillas genéricas ni bloques de color con texto encima.

EL TEXTO (integrado con elegancia sobre la imagen, muy legible y bien jerarquizado, sin faltas de ortografía):
- Título protagonista: "%s"
- %s · %s`, sceneSource, scene, in.Title, in.TypeLabel, in.ModalityText)

	if in.DateText != "" {
		prompt += fmt.Sprintf("\n- Fecha: %s", in.DateText)
	}
	prompt += fmt.Sprintf("\n- Precio: %s", price)
	prompt += fmt.Sprintf("\n- Organizado por: %s", in.BusinessName)

	prompt += `
- Un llamado a la acción claro: "¡Inscríbete ya!" o "Cupos limitados".

REGLAS ESTRICTAS:
- NO incluyas ningún código QR ni recuadros para QR (es una pieza publicitaria, no una escarapela).
- NO entregues una imagen que sea solamente texto sobre fondo plano o degradado: SIEMPRE debe haber una escena/ilustración protagonista de alta calidad.
- MANTÉN EL TEXTO AL MÍNIMO: solo el título, la fecha, el precio y el llamado a la acción. NO escribas la descripción del evento, ni temarios, horarios, listas, párrafos o requisitos dentro de la pieza — eso va en el detalle del catálogo, NO en el afiche. La imagen vende; el texto es solo un titular.
- Todo el texto en español, perfectamente escrito.`

	// Description still feeds context (colors/motifs) on top of the scene.
	return prompt + themeHint(in.Description)
}

// GenerateEventPoster renders a marketing poster (afiche) for the public
// catalog. Same Gemini image path as the badge/certificate, themed by the
// event facts. Same FinOps-label caveat as GenerateEventBadge.
func (s *GeminiService) GenerateEventPoster(ctx context.Context, in PosterInput) ([]byte, error) {
	return s.GeneratePromoBanner(ctx, buildEventPosterPrompt(in), nil)
}

// EventAssetKind identifies which event piece an enhance targets.
type EventAssetKind int

const (
	AssetPoster EventAssetKind = iota
	AssetBadge
	AssetCertificate
)

// buildEventAssetEnhancePrompt instructs Gemini to IMPROVE an existing event
// piece (the image the organizer uploaded or generated) — like the inventory
// photo enhancer, but for design pieces: refine typography, color, composition
// and polish to a professional level WITHOUT changing the textual content,
// names, dates or layout intent. The badge/certificate must keep their QR area.
func buildEventAssetEnhancePrompt(kind EventAssetKind) string {
	base := `Eres un DISEÑADOR GRÁFICO PROFESIONAL retocando una pieza ya existente. La imagen adjunta ES la pieza: respétala como única fuente de verdad de su contenido.

TU TAREA: MEJORAR esta misma pieza a calidad de agencia — refina tipografía, jerarquía, contraste, color, iluminación, composición y nitidez; corrige lo que se vea amateur. El resultado debe verse claramente como la MISMA pieza, solo más profesional.

PROHIBIDO:
- NO cambies el texto, los nombres, las fechas, los precios ni el idioma (español).
- NO inventes ni elimines información; conserva todos los datos que ya aparecen.
- NO agregues marcas de agua ni logos ajenos.`

	switch kind {
	case AssetBadge:
		return base + `
- CONSERVA el recuadro/área reservada para el CÓDIGO QR (no lo borres ni lo tapes).

Es una ESCARAPELA (credencial) vertical: hazla más limpia, legible y elegante, apta para imprimir y para verse en celular.`
	case AssetCertificate:
		return base + `
- CONSERVA el espacio para el código QR de verificación si existe.

Es un CERTIFICADO formal y horizontal: mejora el marco, la tipografía serif y el aspecto digno de imprimir, sin perder solemnidad.`
	default: // poster
		return base + `
- NO agregues ningún código QR (es una pieza publicitaria).
- Mantén el texto al mínimo (título, fecha, precio, llamado a la acción); si hay una escena/foto, mejórala con iluminación y composición de campaña real.

Es un AFICHE PUBLICITARIO vertical: hazlo más llamativo y profesional, listo para redes y catálogo.`
	}
}

// EnhanceEventAsset improves an existing event piece image with Gemini,
// preserving its content (image-to-image). Returns the enhanced bytes.
func (s *GeminiService) EnhanceEventAsset(ctx context.Context, imageData []byte, mimeType string, kind EventAssetKind) ([]byte, error) {
	return s.enhanceImageWithPrompt(ctx, imageData, mimeType,
		buildEventAssetEnhancePrompt(kind), "EVENT_ASSET_ENHANCE")
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
