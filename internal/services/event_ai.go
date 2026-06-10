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

// EventDescriptionInput carries what the organizer answered about the event so
// the AI agent can draft a compelling public description.
type EventDescriptionInput struct {
	Title    string
	Type     string // curso/conferencia/…
	Modality string // presencial/virtual/híbrido
	Audience string // ¿para quién es?
	Includes string // ¿qué incluye / qué aprenderán?
	Level    string // nivel (opcional)
	Extra    string // algo más a destacar (opcional)
	Current  string // descripción actual (si la quiere mejorar)
}

// BuildEventDescriptionPrompt composes the prompt for the description agent.
// Output: copy de catálogo en español neutro (modo USTED), cálido y concreto,
// con markdown ligero (negritas/viñetas) pero sin títulos grandes.
func BuildEventDescriptionPrompt(in EventDescriptionInput) string {
	var b strings.Builder
	b.WriteString("Actúa como un COPYWRITER experto en eventos. Escribe una descripción ATRACTIVA y clara para el catálogo público de un evento, que dé ganas de inscribirse.\n\n")
	b.WriteString("Reglas:\n")
	b.WriteString("- Español neutro de Colombia, en modo USTED (nunca voseo: usa 'descubra', 'aprenda', 'inscríbase').\n")
	b.WriteString("- 2 a 4 párrafos cortos o una mezcla de párrafo + viñetas. Markdown LIGERO: puedes usar **negritas** y viñetas con '- ', NO uses títulos con '#'.\n")
	b.WriteString("- Concreta y honesta: nada de promesas vacías ni relleno. No inventes datos que no te di.\n")
	b.WriteString("- No repitas el precio, la fecha ni el lugar (eso ya se muestra aparte).\n")
	b.WriteString("- Devuelve SOLO la descripción, sin comillas ni encabezados como 'Descripción:'.\n\n")
	b.WriteString("Datos del evento:\n")
	b.WriteString(fmt.Sprintf("- Título: %s\n", strings.TrimSpace(in.Title)))
	if in.Type != "" {
		b.WriteString(fmt.Sprintf("- Tipo: %s\n", in.Type))
	}
	if in.Modality != "" {
		b.WriteString(fmt.Sprintf("- Modalidad: %s\n", in.Modality))
	}
	if s := strings.TrimSpace(in.Audience); s != "" {
		b.WriteString(fmt.Sprintf("- Para quién es: %s\n", s))
	}
	if s := strings.TrimSpace(in.Includes); s != "" {
		b.WriteString(fmt.Sprintf("- Qué incluye / qué aprenderán: %s\n", s))
	}
	if s := strings.TrimSpace(in.Level); s != "" {
		b.WriteString(fmt.Sprintf("- Nivel: %s\n", s))
	}
	if s := strings.TrimSpace(in.Extra); s != "" {
		b.WriteString(fmt.Sprintf("- Otros detalles a destacar: %s\n", s))
	}
	if s := strings.TrimSpace(in.Current); s != "" {
		b.WriteString(fmt.Sprintf("\nMejora y pule esta descripción base manteniendo su intención:\n\"\"\"\n%s\n\"\"\"\n", s))
	}
	return b.String()
}

// buildEventBadgePrompt composes the prompt for an event badge (escarapela).
// The escarapela is a PER-EVENT TEMPLATE reused by every attendee, so it must
// NOT bake any attendee name into the pixels: it reserves a clean NAME band
// (upper-middle) and a QR box (lower) that the public carné view overlays per
// attendee at render time. The design is professional and print-friendly; text
// in Spanish (Art. V). description (optional) themes the piece to the subject.
func buildEventBadgePrompt(eventTitle, businessName, description string) string {
	return fmt.Sprintf(`Diseña una ESCARAPELA (credencial) vertical, moderna y profesional para un evento. Es una PLANTILLA reutilizable: el nombre del asistente y el código QR se sobreponen después, así que NO los dibujes tú.

Datos a mostrar de forma legible:
- Evento: "%s"
- Organizador: "%s"

Requisitos de diseño:
- Estilo limpio y elegante, apto para impresión y para verse en pantalla de celular.
- Tipografía clara y de alto contraste; nada de texto decorativo ilegible.
- Reserva una BANDA horizontal limpia, centrada y de buen contraste en la zona MEDIA-SUPERIOR (su centro alrededor del 40%% de la altura), VACÍA (NO escribas ningún nombre ni el texto "Nombre del Asistente"), donde luego se imprimirá el nombre del asistente.
- Reserva un recuadro blanco CUADRADO, centrado horizontalmente, ubicado en la MITAD INFERIOR (su centro alrededor del 70-75%% de la altura), que ocupe aproximadamente el 40%% del ancho, para el CÓDIGO QR de validación (NO dibujes el QR; deja el espacio en blanco). Debajo del recuadro deja un pequeño margen.
- Paleta sobria con un color de acento; bordes redondeados.
- Todo el texto en español.%s`, eventTitle, businessName, themeHint(description))
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

	prompt := fmt.Sprintf(`Actúa como un DISEÑADOR PUBLICITARIO EXPERTO. Crea un AFICHE PUBLICITARIO PROFESIONAL en PROPORCIÓN 4:5 (vertical, más alto que ancho, formato de afiche), con calidad de agencia de publicidad, para promocionar este evento. Debe verse espectacular en la pantalla de un celular y dar ganas de inscribirse.

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
		return "un AFICHE PUBLICITARIO vertical EN PROPORCIÓN 4:5 (más alto que ancho, formato de afiche), llamativo y profesional, listo para redes y catálogo, SIN código QR y con texto mínimo"
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
func buildEventAssetEnhancePrompt(kind EventAssetKind, brief string, hasFaceRef bool) string {
	noun := assetKindNoun(kind)
	brief = strings.TrimSpace(brief)
	if len(brief) > 600 {
		brief = brief[:600]
	}

	faceRef := ""
	if hasFaceRef {
		faceRef = "\n\nFOTO DE ROSTRO (referencia de identidad): la ÚLTIMA imagen adjunta es una foto clara del ROSTRO de la persona. Úsala como referencia PRINCIPAL para la cara: el rostro del resultado debe ser idéntico al de esa foto (mismos rasgos, ojos, nariz, boca, forma de cara). La primera imagen aporta el cuerpo/escena; el rostro mándalo por la foto de rostro."
	}

	if brief != "" {
		return fmt.Sprintf(`Eres un DISEÑADOR PUBLICITARIO experto. Tienes una FOTO de referencia (adjunta) y debes crear %s para un evento, SIGUIENDO AL PIE DE LA LETRA las indicaciones del organizador.

INDICACIONES DEL ORGANIZADOR (esto es lo que debe mostrar la pieza):
%s

IDENTIDAD DE LA PERSONA (lo más importante):
- La persona protagonista del resultado debe ser RECONOCIBLEMENTE LA MISMA de la foto: mismo rostro, mismos rasgos faciales, mismo tono de piel, mismo color y estilo de cabello, misma complexión.
- NO la reemplaces por otra persona, ni por una versión "genérica", "idealizada" o de stock. NO le cambies la edad, el peinado ni la cara.
- Encuádrala como protagonista, bien iluminada y con el rostro claramente visible y sin deformaciones. Conserva su dignidad (nada caricaturesco).

CÓMO USAR LA FOTO:
- RECREA la escena, el entorno, las poses y los demás elementos según las indicaciones (puedes AGREGAR otras personas —p. ej. alumnos—, objetos, fondo y acciones que pidan las indicaciones). Las personas AÑADIDAS pueden ser genéricas; SOLO la persona de la foto mantiene su identidad exacta.
- IGNORA por completo el fondo y los objetos de la foto que no correspondan a las indicaciones (p. ej. comida, bar, mesa): NO los incluyas.

COMPOSICIÓN Y CALIDAD:
- Fotografía/ilustración profesional, iluminación cinematográfica, profundidad y composición de campaña publicitaria real.
- Deja un espacio claro y equilibrado para el título; jerarquía visual limpia; coherencia de estilo y color.
- Todo el texto en español, perfectamente escrito.%s

Resultado: %s.`, noun, brief, faceRef, noun)
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
// Gemini. images[0] is the base piece; an optional images[1] is a clear face
// photo used to anchor the person's identity. With a brief it RE-CREATES the
// scene per the organizer's instructions; without one it faithfully retouches.
func (s *GeminiService) EnhanceEventAsset(ctx context.Context, kind EventAssetKind, brief string, images []ReferenceImage) ([]byte, error) {
	temp := 0.25
	if strings.TrimSpace(brief) != "" {
		// Permite recrear la escena pero mantiene baja la deriva del rostro;
		// la fidelidad de la identidad la fuerza el prompt, no la temperatura.
		temp = 0.5
	}
	hasFaceRef := len(images) > 1
	return s.enhanceImagesWithPrompt(ctx, images,
		buildEventAssetEnhancePrompt(kind, brief, hasFaceRef), "EVENT_ASSET_ENHANCE", temp)
}

// GenerateEventBadge renders an escarapela design for an event via Gemini and
// returns raw image bytes the caller uploads to storage. It reuses the
// prompt→image path of GeneratePromoBanner (same generationConfig, temperature
// tuned for legible typography). NOTE: FinOps usage is currently logged under
// PROMO_BANNER; a dedicated EVENT_BADGE label (constant already defined) needs
// the image path to be parameterized by feature — tracked as a follow-up.
func (s *GeminiService) GenerateEventBadge(ctx context.Context, eventTitle, businessName, description string) ([]byte, error) {
	return s.GeneratePromoBanner(ctx, buildEventBadgePrompt(eventTitle, businessName, description), nil)
}

// GenerateEventCertificate renders a participation certificate design. Same
// FinOps-label caveat as GenerateEventBadge.
func (s *GeminiService) GenerateEventCertificate(ctx context.Context, eventTitle, businessName, attendeeName, description string) ([]byte, error) {
	return s.GeneratePromoBanner(ctx, buildEventCertificatePrompt(eventTitle, businessName, attendeeName, description), nil)
}
