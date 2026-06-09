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
