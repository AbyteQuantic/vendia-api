// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
