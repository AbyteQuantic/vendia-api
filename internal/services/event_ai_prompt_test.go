// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"strings"
	"testing"
)

func TestBuildEventBadgePrompt_IncludesAnchors(t *testing.T) {
	p := buildEventBadgePrompt("Hackatón VendIA", "Tienda Doña Ana", "Ana Pérez", "")
	low := strings.ToLower(p)
	for _, anchor := range []string{"escarapela", "hackatón vendia", "tienda doña ana", "ana pérez"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("prompt de escarapela no contiene %q:\n%s", anchor, p)
		}
	}
	// Must reserve space for the validation QR (decision #3/#10).
	if !strings.Contains(low, "qr") {
		t.Fatalf("el prompt debe reservar el área del QR:\n%s", p)
	}
}

func TestBuildEventCertificatePrompt_IncludesAnchors(t *testing.T) {
	p := buildEventCertificatePrompt("Curso de Repostería", "Tienda Doña Ana", "Ana Pérez", "")
	low := strings.ToLower(p)
	for _, anchor := range []string{"certificado", "curso de repostería", "ana pérez"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("prompt de certificado no contiene %q:\n%s", anchor, p)
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

// The organizer's description should theme the piece, woven in as context
// (not blank) — and an empty description must not inject a dangling label.
func TestBuildEventBadgePrompt_WeavesDescription(t *testing.T) {
	withDesc := buildEventBadgePrompt("Hackatón VendIA", "Tienda Doña Ana", "Ana Pérez",
		"Maratón de programación con robótica y luces de neón")
	if !strings.Contains(strings.ToLower(withDesc), "robótica") {
		t.Fatalf("la descripción debe alimentar el prompt:\n%s", withDesc)
	}
	if !strings.Contains(strings.ToLower(withDesc), "contexto del evento") {
		t.Fatalf("falta el rótulo de contexto del evento:\n%s", withDesc)
	}

	noDesc := buildEventBadgePrompt("Hackatón VendIA", "Tienda Doña Ana", "Ana Pérez", "   ")
	if strings.Contains(strings.ToLower(noDesc), "contexto del evento") {
		t.Fatalf("descripción vacía no debe inyectar rótulo de contexto:\n%s", noDesc)
	}
}
