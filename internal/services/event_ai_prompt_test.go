// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"strings"
	"testing"
)

func TestBuildEventDescriptionPrompt_WeavesAnswers(t *testing.T) {
	p := BuildEventDescriptionPrompt(EventDescriptionInput{
		Title:    "Curso de color Ámbar",
		Type:     "curso",
		Audience: "estilistas que quieren dominar el tono ámbar",
		Includes: "teoría del color, práctica y kit de muestras",
	})
	low := strings.ToLower(p)
	for _, anchor := range []string{
		"curso de color ámbar", "estilistas", "teoría del color", "modo usted",
	} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("el prompt debe tejer %q:\n%s", anchor, p)
		}
	}
	// No debe pedir repetir precio/fecha (van aparte).
	if !strings.Contains(low, "no repitas el precio") {
		t.Fatalf("debe evitar repetir precio/fecha:\n%s", p)
	}
}

func TestBuildCertificateTextsPrompt_AsksJSONAndWeavesEvent(t *testing.T) {
	p := BuildCertificateTextsPrompt(CertificateTextsInput{
		Title:        "Curso de color Ámbar",
		Type:         "curso",
		Modality:     "presencial",
		Description:  "Formación intensiva en colorimetría.",
		BusinessName: "Don Brayan",
	})
	low := strings.ToLower(p)
	for _, key := range []string{`"title"`, `"intro"`, `"body"`, `"signatory"`, `"footer"`} {
		if !strings.Contains(low, key) {
			t.Fatalf("el prompt debe pedir la clave %s:\n%s", key, p)
		}
	}
	for _, anchor := range []string{"curso de color ámbar", "colorimetría", "don brayan", "modo usted"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("el prompt debe tejer %q:\n%s", anchor, p)
		}
	}
	if !strings.Contains(low, "no incluyas el nombre del asistente") {
		t.Fatalf("debe excluir el nombre del asistente:\n%s", p)
	}
}

func TestBuildEventDescriptionPrompt_ImproveMode(t *testing.T) {
	p := BuildEventDescriptionPrompt(EventDescriptionInput{
		Title:   "Hackatón",
		Current: "Un evento de programación.",
	})
	if !strings.Contains(strings.ToLower(p), "mejora y pule esta descripción base") {
		t.Fatalf("con Current debe entrar en modo mejora:\n%s", p)
	}
}

func TestBuildEventBadgePrompt_IncludesAnchors(t *testing.T) {
	p := buildEventBadgePrompt("Hackatón VendIA", "Tienda Doña Ana", "")
	low := strings.ToLower(p)
	for _, anchor := range []string{"escarapela", "hackatón vendia", "tienda doña ana"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("prompt de escarapela no contiene %q:\n%s", anchor, p)
		}
	}
	// Must reserve space for the validation QR (decision #3/#10).
	if !strings.Contains(low, "qr") {
		t.Fatalf("el prompt debe reservar el área del QR:\n%s", p)
	}
	// Es una PLANTILLA: no debe hornear un nombre de asistente en los píxeles;
	// reserva una banda para sobreponerlo al renderizar.
	if strings.Contains(low, "nombre del asistente") && !strings.Contains(low, "no escribas") {
		t.Fatalf("la escarapela no debe hornear el nombre del asistente:\n%s", p)
	}
	if !strings.Contains(low, "nombre del asistente se imprim") &&
		!strings.Contains(low, "imprimirá el nombre del asistente") {
		t.Fatalf("la escarapela debe reservar una banda para el nombre:\n%s", p)
	}
}

func TestBuildEventCertificatePrompt_FrameOnlyNoText(t *testing.T) {
	p := buildEventCertificatePrompt("Curso de Repostería", "Tienda Doña Ana", "")
	low := strings.ToLower(p)
	if !strings.Contains(low, "certificado") {
		t.Fatalf("debe mencionar certificado:\n%s", p)
	}
	// Es SOLO el marco/fondo: la app pone el texto. Debe prohibir texto y
	// dejar el centro despejado.
	for _, anchor := range []string{
		"no escribas ningún texto", "marca de agua", "centro bien despejado",
	} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("el certificado debe ser solo marco sin texto (%q):\n%s", anchor, p)
		}
	}
}

func TestBuildEventPosterPrompt_SellsAndHasNoQR(t *testing.T) {
	p := buildEventPosterPrompt(PosterInput{
		Title:        "Hackatón VendIA",
		BusinessName: "Tienda Doña Ana",
		Type:         "hackaton",
		TypeLabel:    "Hackatón",
		ModalityText: "Presencial",
		DateText:     "20 de junio de 2026",
		PriceText:    "$50.000",
		Description:  "Maratón de programación con robótica",
	})
	low := strings.ToLower(p)
	// Vende el evento con sus datos clave.
	for _, anchor := range []string{"afiche", "hackatón vendia", "tienda doña ana", "20 de junio de 2026", "$50.000", "robótica"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("el afiche no contiene %q:\n%s", anchor, p)
		}
	}
	// Debe exigir una pieza profesional con escena/personas, no solo texto.
	for _, anchor := range []string{"profesional", "personas", "fotografía o ilustración"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("el afiche debe exigir calidad profesional con escena (%q):\n%s", anchor, p)
		}
	}
	// La pieza NO debe recargarse de texto: el detalle (temario/horarios) va
	// en el catálogo, no en el afiche.
	if !strings.Contains(low, "mantén el texto al mínimo") {
		t.Fatalf("el afiche debe pedir texto mínimo:\n%s", p)
	}
	if !strings.Contains(low, "no escribas la descripción") {
		t.Fatalf("el afiche no debe volcar la descripción/temario:\n%s", p)
	}
	// Es pieza publicitaria: debe PROHIBIR el QR explícitamente y NUNCA pedir
	// que se reserve un recuadro para él (a diferencia de la escarapela).
	if !strings.Contains(low, "no incluyas ningún código qr") {
		t.Fatalf("el afiche debe prohibir explícitamente el QR:\n%s", p)
	}
	if strings.Contains(low, "reserva un recuadro") {
		t.Fatalf("el afiche NO debe reservar área de QR como la escarapela:\n%s", p)
	}
}

func TestBuildEventPosterPrompt_BriefDrivesScene(t *testing.T) {
	p := buildEventPosterPrompt(PosterInput{
		Title:        "Curso de repostería",
		BusinessName: "Dulce Ana",
		Type:         "curso",
		TypeLabel:    "Curso",
		ModalityText: "Presencial",
		Brief:        "manos decorando un pastel con crema, colores pastel",
	})
	low := strings.ToLower(p)
	// El brief del organizador manda sobre la escena por defecto.
	if !strings.Contains(low, "manos decorando un pastel") {
		t.Fatalf("el brief del organizador debe guiar la escena:\n%s", p)
	}
	if !strings.Contains(low, "indicaciones del organizador") {
		t.Fatalf("debe señalar que sigue las indicaciones del organizador:\n%s", p)
	}
}

func TestBuildEventPosterPrompt_DefaultSceneByType(t *testing.T) {
	// Sin brief, la escena por defecto depende del tipo (taller para curso).
	p := buildEventPosterPrompt(PosterInput{
		Title:        "Curso de repostería",
		BusinessName: "Dulce Ana",
		Type:         "curso",
		TypeLabel:    "Curso",
		ModalityText: "Presencial",
	})
	if !strings.Contains(strings.ToLower(p), "instructor") {
		t.Fatalf("curso sin brief debe usar la escena de taller/instructor:\n%s", p)
	}
}

func TestBuildEventPosterPrompt_FreeAndNoDate(t *testing.T) {
	p := buildEventPosterPrompt(PosterInput{
		Title:        "Charla abierta",
		BusinessName: "Academia X",
		Type:         "conferencia",
		TypeLabel:    "Conferencia",
		ModalityText: "Virtual",
		// Sin fecha ni precio → "Gratis", sin línea de fecha.
	})
	low := strings.ToLower(p)
	if !strings.Contains(low, "gratis") {
		t.Fatalf("precio vacío debe leerse Gratis:\n%s", p)
	}
	if strings.Contains(low, "\n- fecha:") {
		t.Fatalf("sin fecha no debe inyectar rótulo de fecha:\n%s", p)
	}
}

func TestBuildEventAssetEnhancePrompt_BriefTransformsKeepingFace(t *testing.T) {
	p := buildEventAssetEnhancePrompt(AssetPoster, "La docente enseñando a un grupo de alumnas a aplicar tinte ámbar", false)
	low := strings.ToLower(p)
	// Sigue las indicaciones del organizador…
	if !strings.Contains(low, "alumnas a aplicar tinte ámbar") {
		t.Fatalf("el enhance con brief debe seguir las indicaciones:\n%s", p)
	}
	// …y exige conservar la identidad/rostro de la persona de la foto.
	for _, anchor := range []string{"misma de la foto", "mismo rostro", "no la reemplaces"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("el enhance debe exigir conservar el rostro (%q):\n%s", anchor, p)
		}
	}
}

func TestBuildEventAssetEnhancePrompt_FaceRefAnchorsIdentity(t *testing.T) {
	p := buildEventAssetEnhancePrompt(AssetPoster, "la docente con sus alumnas", true)
	low := strings.ToLower(p)
	if !strings.Contains(low, "foto de rostro") || !strings.Contains(low, "última imagen") {
		t.Fatalf("con 2ª foto el prompt debe usar el rostro de referencia:\n%s", p)
	}
	// Sin face ref no debe mencionarlo.
	p2 := buildEventAssetEnhancePrompt(AssetPoster, "la docente con sus alumnas", false)
	if strings.Contains(strings.ToLower(p2), "última imagen") {
		t.Fatalf("sin 2ª foto no debe mencionar la imagen de rostro:\n%s", p2)
	}
}

func TestBuildEventAssetEnhancePrompt_NoBriefIsFaithfulRetouch(t *testing.T) {
	p := buildEventAssetEnhancePrompt(AssetBadge, "", false)
	low := strings.ToLower(p)
	// Sin brief: retoque fiel, no transforma.
	if !strings.Contains(low, "mejorar esta misma pieza") {
		t.Fatalf("sin brief debe ser retoque fiel:\n%s", p)
	}
	if !strings.Contains(low, "qr") {
		t.Fatalf("la escarapela debe conservar el QR:\n%s", p)
	}
}

// The organizer's description should theme the piece, woven in as context
// (not blank) — and an empty description must not inject a dangling label.
func TestBuildEventBadgePrompt_WeavesDescription(t *testing.T) {
	withDesc := buildEventBadgePrompt("Hackatón VendIA", "Tienda Doña Ana",
		"Maratón de programación con robótica y luces de neón")
	if !strings.Contains(strings.ToLower(withDesc), "robótica") {
		t.Fatalf("la descripción debe alimentar el prompt:\n%s", withDesc)
	}
	if !strings.Contains(strings.ToLower(withDesc), "contexto del evento") {
		t.Fatalf("falta el rótulo de contexto del evento:\n%s", withDesc)
	}

	noDesc := buildEventBadgePrompt("Hackatón VendIA", "Tienda Doña Ana", "   ")
	if strings.Contains(strings.ToLower(noDesc), "contexto del evento") {
		t.Fatalf("descripción vacía no debe inyectar rótulo de contexto:\n%s", noDesc)
	}
}
