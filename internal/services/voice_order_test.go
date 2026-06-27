// Spec: specs/085-vender-por-voz/spec.md
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Orden compuesta: parsea comandos ordenados (mesa primero, luego items).
func TestParseVoiceOrderJSON_Compound(t *testing.T) {
	raw := `{"commands":[
		{"action":"fijar_mesa","item":null,"quantity":null,"target":{"type":"mesa","mesa":"3"},"confidence":0.97,"raw":"para la mesa 3"},
		{"action":"agregar","item":"aguila","quantity":2,"confidence":0.95,"raw":"dos aguilas"},
		{"action":"quitar","item":"gaseosa","quantity":null,"confidence":0.9,"raw":"quite la gaseosa"}
	],"transcript":"...","clarify_prompt":null}`
	res, err := ParseVoiceOrderJSON(raw)
	require.NoError(t, err)
	require.Len(t, res.Commands, 3)
	assert.Equal(t, "fijar_mesa", res.Commands[0].Action)
	assert.Equal(t, "mesa", res.Commands[0].Target.Type)
	assert.Equal(t, "3", res.Commands[0].Target.Mesa)
	assert.Equal(t, "agregar", res.Commands[1].Action)
	assert.Equal(t, 2, *res.Commands[1].Quantity)
	assert.Equal(t, "aguila", *res.Commands[1].Item)
	assert.Equal(t, "quitar", res.Commands[2].Action)
	assert.Nil(t, res.Commands[2].Quantity) // quitar todo
	assert.False(t, res.Degraded)
}

// Tolera fences markdown + texto suelto alrededor del objeto.
func TestParseVoiceOrderJSON_MarkdownAndStrayText(t *testing.T) {
	raw := "```json\nClaro: {\"commands\":[{\"action\":\"agregar\",\"item\":\"pan\",\"quantity\":1,\"raw\":\"un pan\"}],\"transcript\":\"un pan\"}\n```"
	res, err := ParseVoiceOrderJSON(raw)
	require.NoError(t, err)
	require.Len(t, res.Commands, 1)
	assert.Equal(t, "pan", *res.Commands[0].Item)
}

// Acciones inválidas se descartan (anti-alucinación); cantidades negativas → 0.
func TestParseVoiceOrderJSON_SanitizesActionsAndQty(t *testing.T) {
	raw := `{"commands":[
		{"action":"borrar_base_de_datos","item":"x","raw":"hack"},
		{"action":"AGREGAR","item":"cafe","quantity":-5,"confidence":2.0,"raw":"un cafe"}
	]}`
	res, err := ParseVoiceOrderJSON(raw)
	require.NoError(t, err)
	require.Len(t, res.Commands, 1, "acción desconocida descartada")
	assert.Equal(t, "agregar", res.Commands[0].Action) // normaliza case
	assert.Equal(t, 0, *res.Commands[0].Quantity)      // negativa → 0
	assert.Equal(t, 1.0, res.Commands[0].Confidence)   // clamp a 1
}

// Audio vacío / sin comandos → resultado vacío sin error (no rompe la venta).
func TestParseVoiceOrderJSON_Empty(t *testing.T) {
	res, err := ParseVoiceOrderJSON(`{"commands":[],"clarify_prompt":"¿Qué desea agregar?"}`)
	require.NoError(t, err)
	assert.Empty(t, res.Commands)
	require.NotNil(t, res.ClarifyPrompt)

	res2, err2 := ParseVoiceOrderJSON("")
	require.NoError(t, err2)
	assert.Empty(t, res2.Commands)
}

// JSON ilegible → error (el handler lo traduce a degraded).
func TestParseVoiceOrderJSON_Garbage(t *testing.T) {
	_, err := ParseVoiceOrderJSON("no soy json {{{")
	assert.Error(t, err)
}
