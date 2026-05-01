package services

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// NormalizeText strips accents, lowercases, and collapses whitespace.
// Used for fuzzy product name comparison.
func NormalizeText(s string) string {
	// NFD decompose → remove combining marks → NFC recompose
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ := transform.String(t, s)
	result = strings.ToLower(result)
	// Collapse whitespace
	fields := strings.Fields(result)
	return strings.Join(fields, " ")
}
