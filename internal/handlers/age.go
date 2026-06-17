// Spec: specs/063-control-edad-18/spec.md
package handlers

import (
	"strings"
	"time"
)

// adultAge es la mayoría de edad en Colombia: 18 años cumplidos.
const adultAge = 18

// computeAgeISO calcula la edad en años cumplidos a partir de una fecha de
// nacimiento en formato ISO "yyyy-mm-dd". Devuelve (edad, true) si la cadena
// es una fecha de calendario válida y pasada; (0, false) en cualquier otro
// caso (cadena vacía, formato inválido, día inexistente o fecha futura).
//
// Es el equivalente server-side de admin-web/src/lib/age.ts:computeAge — la
// verificación NO puede vivir solo en el cliente: un comprador podría saltarse
// el JS, así que el backend la repite como defensa en profundidad.
func computeAgeISO(birthISO string, now time.Time) (int, bool) {
	s := strings.TrimSpace(birthISO)
	// time.Parse con este layout rechaza días/meses fuera de rango
	// (p. ej. "2020-02-31" → error), así que valida el calendario por nosotros.
	birth, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, false
	}

	age := now.Year() - birth.Year()
	// Si el cumpleaños aún no llega este año, resta uno.
	if now.Month() < birth.Month() ||
		(now.Month() == birth.Month() && now.Day() < birth.Day()) {
		age--
	}

	// Edad negativa ⇒ fecha futura ⇒ no es una fecha de nacimiento real.
	if age < 0 {
		return 0, false
	}
	return age, true
}

// isAdultISO devuelve true si la persona tiene al menos 18 años cumplidos en
// `now`. Una fecha vacía, inválida o futura cuenta como NO mayor de edad
// (fail-closed) — así un pedido con productos +18 sin fecha válida se rechaza.
func isAdultISO(birthISO string, now time.Time) bool {
	age, ok := computeAgeISO(birthISO, now)
	return ok && age >= adultAge
}
