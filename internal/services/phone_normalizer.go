// Spec: specs/026-importador-clientes/spec.md
package services

import "strings"

// NormalizePhone extracts only digit characters from s and returns the
// resulting string. If fewer than 7 digits remain after stripping, an empty
// string is returned — not enough digits to constitute a valid phone number.
// Colombian mobile numbers are 10 digits; international form adds a 2-digit
// country code (+57). Seven digits covers short local numbers and edge-cases
// from the spec (§9).
func NormalizePhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	result := b.String()
	if len(result) < 7 {
		return ""
	}
	return result
}
