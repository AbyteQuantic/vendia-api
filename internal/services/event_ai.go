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

// assetKindNoun returns the Spanish piece name + framing per kind.
func assetKindNoun(kind EventAssetKind) string {
	switch kind {
	case AssetBadge:
		return "una ESCARAPELA (credencial) vertical, limpia y legible; CONSERVA el recuadro/área del CÓDIGO QR"
	case AssetCertificate:
		return "un CERTIFICADO formal y horizontal, elegante; conserva el espacio del QR de verificación si existe"
	default:
		return "un AFICHE PUBLICITARIO vertical, llamativo y profesional, listo para redes y catálogo, SIN código QR y con texto mínimo"
	}
}

// buildEventAssetEnhancePrompt instructs Gemini on the image-to-image step.
// Two very different modes depending on whether the organizer wrote a brief:
//   - WITH brief → an INSTRUCTED TRANSFORM: use the attached photo as visual
//     reference (keep the people's likeness) but RE-CREATE the scene following
//     the organizer's instructions (e.g., "la docente enseñando a un grupo de
//     alumnos"). This is what fixes "subí una foto y la IA hizo algo distinto".
//   - WITHOUT brief → a faithful RETOUCH: only improve quality, never change
//     the content.
func buildEventAssetEnhancePrompt(kind EventAssetKind, brief string) string {
	noun := assetKindNoun(kind)
	brief = strings.TrimSpace(brief)
	if len(brief) > 600 {
		brief = brief[:600]
	}

	if brief != "" {
		return fmt.Sprintf(`Eres un DISEÑADOR PUBLICITARIO experto. Tienes una FOTO de referencia (adjunta) y debes crear %s para un evento, SIGUIENDO AL PIE DE LA LETRA las indicaciones del organizador.

INDICACIONES DEL ORGANIZADOR (esto es lo que debe mostrar la pieza):
%s

CÓMO USAR LA FOTO:
- Usa a la(s) PERSONA(S) de la foto como protagonista(s): conserva su parecido, rostro y rasgos reconocibles.
- RECREA la escena, el entorno, las poses y los demás elementos según las indicaciones (puedes agregar personas, objetos, fondo y acciones que pidan las indicaciones). NO te limites a la escena original de la foto.
- Ignora el fondo o los objetos de la foto que no correspondan a las indicaciones (p. ej. comida, bar): no los incluyas.

CALIDAD: fotografía/ilustración profesional, iluminación cinematográfica, composición de campaña real, español sin faltas. Resultado: %s.`, noun, brief, noun)
	}

	base := `Eres un DISEÑADOR GRÁFICO PROFESIONAL retocando una pieza ya existente. La imagen adjunta ES la pieza: respétala como única fuente de verdad de su contenido.

TU TAREA: MEJORAR esta misma pieza a calidad de agencia — refina tipografía, jerarquía, contraste, color, iluminación, composición y nitidez. El resultado debe verse como la MISMA pieza, solo más profesional.

PROHIBIDO:
- NO cambies el texto, los nombres, las fechas ni los precios.
- NO inventes ni elimines información; conserva todos los datos que ya aparecen.`

	switch kind {
	case AssetBadge:
		return base + "\n- CONSERVA el recuadro del CÓDIGO QR.\n\nEs una escarapela vertical: hazla más limpia y legible."
	case AssetCertificate:
		return base + "\n- CONSERVA el espacio del QR si existe.\n\nEs un certificado formal: mejora marco y tipografía serif."
	default:
		return base + "\n- NO agregues QR; mantén el texto al mínimo.\n\nEs un afiche publicitario: hazlo más llamativo y profesional."
	}
}

// EnhanceEventAsset improves/transforms an existing event piece image with
// Gemini. With a brief it RE-CREATES the scene per the organizer's
// instructions (higher temperature); without one it faithfully retouches.
func (s *GeminiService) EnhanceEventAsset(ctx context.Context, imageData []byte, mimeType string, kind EventAssetKind, brief string) ([]byte, error) {
	temp := 0.25
	if strings.TrimSpace(brief) != "" {
		temp = 0.65 // permite recrear la escena siguiendo las indicaciones
	}
	return s.enhanceImageWithPrompt(ctx, imageData, mimeType,
		buildEventAssetEnhancePrompt(kind, brief), "EVENT_ASSET_ENHANCE", temp)
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
