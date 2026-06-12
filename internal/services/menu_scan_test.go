// Spec: specs/043-menu-restaurante-recetas/spec.md
package services

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Parseo del JSON que devuelve Gemini para el scan de menú.
func TestMenuScanResult_Parse(t *testing.T) {
	raw := `{"dishes":[{"name":"Bandeja Paisa","description":"Frijoles, arroz, carne","price":25000,"portion":"Personal","category":"Platos fuertes"},{"name":"Limonada de Coco","description":"","price":8000,"portion":"","category":"Bebidas"}]}`
	var r MenuScanResult
	require.NoError(t, json.Unmarshal([]byte(stripMarkdownJSON(raw)), &r))
	require.Len(t, r.Dishes, 2)
	assert.Equal(t, "Bandeja Paisa", r.Dishes[0].Name)
	assert.Equal(t, float64(25000), r.Dishes[0].Price)
	assert.Equal(t, "Bebidas", r.Dishes[1].Category)
}

func TestMenuScanResult_ParseWithMarkdownFence(t *testing.T) {
	raw := "```json\n{\"dishes\":[{\"name\":\"Arepa\",\"price\":3000,\"category\":\"Entradas\"}]}\n```"
	var r MenuScanResult
	require.NoError(t, json.Unmarshal([]byte(stripMarkdownJSON(raw)), &r))
	require.Len(t, r.Dishes, 1)
	assert.Equal(t, "Arepa", r.Dishes[0].Name)
}
