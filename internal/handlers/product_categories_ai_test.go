// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCategorySuggestions_FiltersAndValidates(t *testing.T) {
	names := map[string]string{"p1": "Lubricante Neutro", "p2": "Perfume Dama"}
	text := "```json\n{\"items\":[" +
		"{\"id\":\"p1\",\"category\":\"Lubricantes\"}," +
		"{\"id\":\"p2\",\"category\":\"Perfumes\"}," +
		"{\"id\":\"fantasma\",\"category\":\"X\"}," + // id inventado → descartar
		"{\"id\":\"p1\",\"category\":\"Otra\"}," + // duplicado → descartar
		"{\"id\":\"p2\",\"category\":\"\"}]}\n```" // ya estaba p2; vacío igual no cuenta
	out := parseCategorySuggestions(text, names)
	assert.Len(t, out, 2)
	assert.Equal(t, "p1", out[0]["id"])
	assert.Equal(t, "Lubricantes", out[0]["suggested"])
	assert.Equal(t, "Perfumes", out[1]["suggested"])
}

// Auditoría 2026-07-10 — BUG: el recorte a maxCategoryLen era por BYTES
// (cat[:40]), y una categoría en español con tildes/eñes puede partir una
// runa UTF-8 justo en el byte 40 → se persistía/mostraba mojibake (U+FFFD).
// El recorte debe ser por runas y producir SIEMPRE un string UTF-8 válido.
func TestParseCategorySuggestions_TruncatesOnRuneBoundary(t *testing.T) {
	names := map[string]string{"p1": "Olla a presión"}
	// 39 bytes ASCII + multibyte: el byte 40 cae a mitad de la "é".
	long := strings.Repeat("a", 39) + "ééééé"
	text := `{"items":[{"id":"p1","category":"` + long + `"}]}`

	out := parseCategorySuggestions(text, names)
	require.Len(t, out, 1)
	got := out[0]["suggested"].(string)
	assert.True(t, utf8.ValidString(got),
		"la categoría recortada jamás puede quedar con UTF-8 inválido: %q", got)
	assert.LessOrEqual(t, len([]rune(got)), maxCategoryLen)
}
