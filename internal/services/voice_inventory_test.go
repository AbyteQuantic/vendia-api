package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVoiceInventoryJSON_HappyPath(t *testing.T) {
	raw := `[
		{"name": "Coca Cola 350ml", "quantity": 12, "price": 2500},
		{"name": "Papas Margarita", "quantity": 5, "price": 3000}
	]`

	items, err := ParseVoiceInventoryJSON(raw)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "Coca Cola 350ml", items[0].Name)
	assert.Equal(t, 12, items[0].Quantity)
	assert.EqualValues(t, 2500, items[0].Price)
}

func TestParseVoiceInventoryJSON_StripsMarkdownFences(t *testing.T) {
	raw := "```json\n[{\"name\":\"Arroz Diana\",\"quantity\":3,\"price\":2900}]\n```"

	items, err := ParseVoiceInventoryJSON(raw)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Arroz Diana", items[0].Name)
}

func TestParseVoiceInventoryJSON_UnwrapsObjectFallback(t *testing.T) {
	// Some LLM runs forget the "array only" instruction — accept a
	// wrapping object as long as the array is recoverable from it.
	raw := `{"products": [{"name":"Aceite","quantity":2,"price":6500}]}`

	items, err := ParseVoiceInventoryJSON(raw)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Aceite", items[0].Name)
}

func TestParseVoiceInventoryJSON_StripsStraySurroundingText(t *testing.T) {
	raw := "Claro, aquí está:\n[{\"name\":\"Leche\",\"quantity\":1,\"price\":4500}]\n¡Listo!"
	items, err := ParseVoiceInventoryJSON(raw)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Leche", items[0].Name)
}

func TestParseVoiceInventoryJSON_EmptyStringReturnsEmptySlice(t *testing.T) {
	items, err := ParseVoiceInventoryJSON("")
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestParseVoiceInventoryJSON_DropsNamelessEntries(t *testing.T) {
	raw := `[
		{"name": "", "quantity": 5, "price": 100},
		{"name": "   ", "quantity": 2, "price": 50},
		{"name": "Huevos", "quantity": 12, "price": 600}
	]`
	items, err := ParseVoiceInventoryJSON(raw)
	require.NoError(t, err)
	require.Len(t, items, 1, "nameless rows must be filtered out")
	assert.Equal(t, "Huevos", items[0].Name)
}

func TestParseVoiceInventoryJSON_ClampsNegativesToZero(t *testing.T) {
	// LLMs occasionally emit negatives when the audio is ambiguous
	// ("menos dos..."). The review UI would render "-2 UND" which is
	// bad UX; clamp to 0 so the user adjusts upward manually.
	raw := `[{"name":"Pan","quantity":-3,"price":-500}]`
	items, err := ParseVoiceInventoryJSON(raw)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, 0, items[0].Quantity)
	assert.EqualValues(t, 0, items[0].Price)
}

func TestParseVoiceInventoryJSON_InvalidJSONReturnsError(t *testing.T) {
	_, err := ParseVoiceInventoryJSON("not json at all")
	assert.Error(t, err)
}

func TestIsSupportedAudioMimeType(t *testing.T) {
	cases := map[string]bool{
		"audio/m4a":         true,
		"audio/mp3":         true,
		"AUDIO/WAV":         true, // case-insensitive
		" audio/webm ":      true, // trimmed
		"audio/flac":        false,
		"image/png":         false,
		"":                  false,
		"application/octet": false,
	}
	for mime, want := range cases {
		t.Run(mime, func(t *testing.T) {
			assert.Equal(t, want, IsSupportedAudioMimeType(mime))
		})
	}
}

func TestVoiceInventoryPrompt_IsStable(t *testing.T) {
	// Anti-hallucination guardrails the 2026-04-24 incident forced
	// into the prompt. Losing any of these reopens the
	// "Arroz Roa / Aceite Premier / Huevos AAA" class of bug.
	assert.Contains(t, VoiceInventoryPrompt, "NO inventes productos")
	assert.Contains(t, VoiceInventoryPrompt, "arreglo vacío: []")
	assert.Contains(t, VoiceInventoryPrompt, "NUNCA uses ejemplos predeterminados")
	assert.Contains(t, VoiceInventoryPrompt, "NO inventes marcas ni presentaciones")
	// Output format lock.
	assert.Contains(t, VoiceInventoryPrompt, "NO uses bloques de código markdown")
	assert.Contains(t, VoiceInventoryPrompt, `[{"name": "string", "quantity": int, "price": float}]`)
	// The prompt must NOT embed any sample product name — Gemini
	// would anchor on the first example and hallucinate around it
	// whenever the audio is unclear. No brand, no product category.
	forbiddenExamples := []string{
		"Coca Cola", "Arroz", "Aceite", "Huevos", "Empanada",
	}
	for _, sample := range forbiddenExamples {
		assert.NotContains(t, VoiceInventoryPrompt, sample,
			"prompt must not seed %q — the model anchors on examples", sample)
	}
}

func TestIsSupportedAudioMimeType_StripsParameters(t *testing.T) {
	// Dio's MultipartFile occasionally ships with a Content-Type that
	// includes parameters ("audio/m4a; charset=utf-8"). Without strip
	// the map lookup fails and the handler 400s with "formato de audio
	// no soportado" — which the Flutter catch currently masks as the
	// generic "No se pudo procesar." toast.
	cases := map[string]bool{
		"audio/m4a; charset=utf-8": true,
		"audio/webm;codecs=opus":   true,
		"AUDIO/WAV; boundary=---":  true,
		"audio/flac; charset=utf-8": false,
	}
	for mt, want := range cases {
		t.Run(mt, func(t *testing.T) {
			assert.Equal(t, want, IsSupportedAudioMimeType(mt))
		})
	}
}

func TestExtractVoiceInventory_RejectsUnsupportedMimeType(t *testing.T) {
	svc := &GeminiService{apiKey: "x", model: "m"}
	_, err := svc.ExtractVoiceInventory(nil, []byte{0x01, 0x02}, "audio/flac")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported audio mime type")
}

func TestExtractVoiceInventory_RejectsNilReceiver(t *testing.T) {
	var svc *GeminiService
	_, err := svc.ExtractVoiceInventory(nil, []byte{}, "audio/m4a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}
