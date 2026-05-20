// Spec: specs/027-importador-inventario/spec.md
package services

import (
	"fmt"
	"strconv"
	"strings"
)

// NormalizePriceCOP parses a Colombian-formatted price string into a float64.
//
// Accepted formats (FR-10, spec §9):
//   - "1500"         → 1500.0
//   - "$1500"        → 1500.0
//   - "$ 1.500"      → 1500.0  (dot = thousands separator, COP style)
//   - "1.500"        → 1500.0  (dot in \d{1,3}(\.\d{3})+ position → thousands)
//   - "1,500"        → 1500.0  (comma in \d{1,3}(,\d{3})+ position → thousands)
//   - "1500.50"      → 1500.5  (single dot not in thousands position → decimal)
//   - "1.500,50"     → 1500.5  (European: dot=thousands, comma=decimal)
//   - "1,500.00"     → 1500.0  (US: comma=thousands, dot=decimal)
//   - "1,5"          → 1.5     (single comma not in thousands position → decimal)
//
// Returns an error if:
//   - the string cannot be parsed as a number
//   - the resulting value is <= 0
func NormalizePriceCOP(s string) (float64, error) {
	// Strip leading/trailing whitespace and currency symbols
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimSpace(s)
	// Remove any remaining non-numeric prefix/suffix characters that are
	// not digits, dot, comma, or minus sign.
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, fmt.Errorf("precio vacío")
	}

	hasDot := strings.ContainsRune(s, '.')
	hasComma := strings.ContainsRune(s, ',')

	var normalized string

	switch {
	case hasDot && hasComma:
		// Both separators present. The rightmost one is the decimal separator;
		// the other is the thousands separator.
		// Examples: "1.500,50" (European), "1,500.00" (US)
		lastDot := strings.LastIndex(s, ".")
		lastComma := strings.LastIndex(s, ",")
		if lastComma > lastDot {
			// comma is decimal → dot is thousands
			// "1.500,50" → remove dots → "1500,50" → replace comma with dot → "1500.50"
			normalized = strings.ReplaceAll(s, ".", "")
			normalized = strings.ReplaceAll(normalized, ",", ".")
		} else {
			// dot is decimal → comma is thousands
			// "1,500.00" → remove commas → "1500.00"
			normalized = strings.ReplaceAll(s, ",", "")
		}

	case hasDot && !hasComma:
		// Only dot present.
		// Heuristic: if the string matches \d{1,3}(\.\d{3})+ pattern, the dot
		// is a thousands separator (Colombian/European style). Otherwise treat
		// it as a decimal point.
		if isThousandsDotPattern(s) {
			normalized = strings.ReplaceAll(s, ".", "")
		} else {
			normalized = s
		}

	case !hasDot && hasComma:
		// Only comma present.
		// Same heuristic: if matches \d{1,3}(,\d{3})+ → thousands; else decimal.
		if isThousandsCommaPattern(s) {
			normalized = strings.ReplaceAll(s, ",", "")
		} else {
			normalized = strings.ReplaceAll(s, ",", ".")
		}

	default:
		// No separator — plain integer string
		normalized = s
	}

	val, err := strconv.ParseFloat(normalized, 64)
	if err != nil {
		return 0, fmt.Errorf("precio inválido: %q no es un número válido", s)
	}

	if val <= 0 {
		return 0, fmt.Errorf("precio debe ser mayor a 0 (recibido: %v)", val)
	}

	return val, nil
}

// isThousandsDotPattern reports whether s matches the pattern where dots are
// thousands separators: one to three digits, followed by one or more groups
// of exactly three digits preceded by a dot.
// Examples: "1.500", "1.500.000", "12.345.678"
func isThousandsDotPattern(s string) bool {
	// Remove a potential minus sign for the check
	check := strings.TrimPrefix(s, "-")
	parts := strings.Split(check, ".")
	if len(parts) < 2 {
		return false
	}
	// First group: 1-3 digits
	if len(parts[0]) == 0 || len(parts[0]) > 3 {
		return false
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return false
		}
	}
	// All subsequent groups must be exactly 3 digits
	for _, p := range parts[1:] {
		if len(p) != 3 {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// isThousandsCommaPattern is the comma equivalent of isThousandsDotPattern.
// Examples: "1,500", "1,500,000"
func isThousandsCommaPattern(s string) bool {
	check := strings.TrimPrefix(s, "-")
	parts := strings.Split(check, ",")
	if len(parts) < 2 {
		return false
	}
	if len(parts[0]) == 0 || len(parts[0]) > 3 {
		return false
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return false
		}
	}
	for _, p := range parts[1:] {
		if len(p) != 3 {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
