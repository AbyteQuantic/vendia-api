// Spec: specs/082-catalogo-online-personalizacion/spec.md
package handlers

import "testing"

func TestSanitizeHexColor(t *testing.T) {
	cases := map[string]string{
		"":             "",          // limpiar
		"  ":           "",          // espacios → limpiar
		"#1A2FA0":      "#1A2FA0",   // RRGGBB ok
		"#aabbccdd":    "#aabbccdd", // AARRGGBB ok
		" #1a2fa0 ":    "#1a2fa0",   // trim
		"1A2FA0":       "",          // sin # → rechaza
		"#12345":       "",          // largo inválido
		"#GGGGGG":      "",          // no-hex → rechaza
		"rojo":         "",          // texto → rechaza
		"#1A2FA0; rm":  "",          // inyección → rechaza
	}
	for in, want := range cases {
		if got := sanitizeHexColor(in); got != want {
			t.Errorf("sanitizeHexColor(%q) = %q, quería %q", in, got, want)
		}
	}
}
