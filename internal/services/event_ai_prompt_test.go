// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"strings"
	"testing"
)

func TestBuildEventBadgePrompt_IncludesAnchors(t *testing.T) {
	p := buildEventBadgePrompt("Hackatón VendIA", "Tienda Doña Ana", "Ana Pérez")
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
	p := buildEventCertificatePrompt("Curso de Repostería", "Tienda Doña Ana", "Ana Pérez")
	low := strings.ToLower(p)
	for _, anchor := range []string{"certificado", "curso de repostería", "ana pérez"} {
		if !strings.Contains(low, anchor) {
			t.Fatalf("prompt de certificado no contiene %q:\n%s", anchor, p)
		}
	}
}
