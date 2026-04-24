package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"strings"
)

// VoiceInventoryItem mirrors the Phase-4 contract the tendero's voice
// note resolves into: a product name, a quantity (0 if not mentioned),
// and a price in COP (0 if not mentioned). Kept flat so the frontend
// can feed the same array into the existing IAResultScreen review UI.
type VoiceInventoryItem struct {
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

// VoiceInventoryPrompt is the system prompt the brief prescribes.
// Exported so tests can assert it stayed aligned with the Flutter
// expectations — any drift between the brief and this constant is a
// regression we want to catch at test time.
//
// Hardening (2026-04-23): the original prompt let Gemini wrap the
// array in ```json fences. The parser handles that, but the stricter
// wording below reduces the incidence outright and removes a failure
// mode when the model emits stray prose around the fence.
const VoiceInventoryPrompt = `Eres un asistente de inventario para tenderos colombianos. Escucha el audio y extrae una lista de productos.

Reglas estrictas de salida:
- Responde ÚNICA Y EXCLUSIVAMENTE con un JSON Array válido.
- NO uses bloques de código markdown. NO uses backticks (` + "```" + `). NO escribas la palabra "json" antes del arreglo.
- NO agregues texto, saludos, ni explicaciones antes o después del arreglo.
- Formato estricto: [{"name": "string", "quantity": int, "price": float}]
- Si el usuario menciona cantidades y precios, asígnalos; si omite el precio, ponlo en 0.

Ejemplo válido de salida: [{"name":"Coca Cola 350ml","quantity":12,"price":2500}]`

// Supported audio MIME types accepted by Gemini multimodal. See
//   https://ai.google.dev/gemini-api/docs/audio
// Rejecting anything outside this set at the handler layer gives the
// tendero a clear error instead of a cryptic 500 from the model.
var SupportedAudioMimeTypes = map[string]struct{}{
	"audio/mp3":  {},
	"audio/mpeg": {},
	"audio/wav":  {},
	"audio/x-wav": {},
	"audio/webm": {},
	"audio/ogg":  {},
	"audio/aac":  {},
	"audio/m4a":  {},
	"audio/mp4":  {},
	"audio/x-m4a": {},
}

// IsSupportedAudioMimeType is the exported predicate the handler uses.
// Kept as a function (not a direct map lookup) so callers don't need
// to understand the internal representation.
//
// Robust against parameters: when Dio's MultipartFile ships a part with
// a Content-Type like "audio/m4a; charset=utf-8" the raw map lookup
// would miss — the header value includes parameters that never belong
// in the key. We normalise via mime.ParseMediaType, fall back to a
// manual split when that fails (odd vendor strings), and lower-case
// for the final check.
func IsSupportedAudioMimeType(mimeType string) bool {
	normalised := normaliseMimeType(mimeType)
	if normalised == "" {
		return false
	}
	_, ok := SupportedAudioMimeTypes[normalised]
	return ok
}

// normaliseMimeType trims whitespace, drops parameters after the first
// `;`, and lower-cases the result. Exported via tests only — callers
// should prefer IsSupportedAudioMimeType.
func normaliseMimeType(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(s); err == nil {
		return strings.ToLower(mediaType)
	}
	// mime.ParseMediaType rejects some browsers' malformed params.
	// Fall back to a simple split so a misbehaving client can still
	// get through when the type itself is fine.
	if idx := strings.Index(s, ";"); idx >= 0 {
		s = s[:idx]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// ExtractVoiceInventory sends the raw audio to Gemini multimodal with
// the Phase-4 prompt and returns the parsed product list. Errors are
// classified so the caller can 422 on LLM-parse failures vs. 503 on
// transport failures — but we keep the API simple: one error type,
// the handler maps it to a single 422 payload.
func (s *GeminiService) ExtractVoiceInventory(
	ctx context.Context,
	audioData []byte,
	mimeType string,
) ([]VoiceInventoryItem, error) {
	if s == nil {
		return nil, fmt.Errorf("gemini service not configured")
	}
	if !IsSupportedAudioMimeType(mimeType) {
		return nil, fmt.Errorf("unsupported audio mime type: %s", mimeType)
	}

	// callWithImage is misleadingly named — the underlying Gemini
	// inlineData schema accepts any binary payload (image, audio,
	// video). Reusing it avoids a parallel implementation that would
	// drift over time. The request still goes to s.model (text model)
	// because the multimodal capability lives on gemini-2.0-flash /
	// gemini-1.5-flash — the same model we already use for OCR.
	raw, err := s.callWithImage(ctx, audioData, mimeType, VoiceInventoryPrompt)
	if err != nil {
		return nil, err
	}

	items, err := ParseVoiceInventoryJSON(raw)
	if err != nil {
		log.Printf("[VOICE] parse error: %v | raw=%.300s", err, raw)
		return nil, err
	}
	return items, nil
}

// ParseVoiceInventoryJSON is the pure-function decoder. Exported so
// tests can exercise prompt-response parsing without spinning up an
// HTTP server. Handles:
//   - Raw arrays: `[{"name": ...}]`
//   - Markdown-fenced arrays: "```json\n[...]\n```"
//   - Accidental object wrapper: `{"products": [...]}`  — some LLMs
//     forget the "array only" instruction under low-confidence audio.
//   - Stray text before/after the array (strips to the first '[' and
//     last ']').
//
// Negative quantities / prices are clamped to 0 — the frontend
// re-renders anything > 0 as-is, and a negative would surface as an
// awkward "-2 UND" in the review UI.
func ParseVoiceInventoryJSON(raw string) ([]VoiceInventoryItem, error) {
	cleaned := stripMarkdownFences(raw)
	if cleaned == "" {
		return []VoiceInventoryItem{}, nil
	}

	// Try extracting the array slice first — robust against both
	// raw arrays and stray surrounding text ("Claro, aquí va: [...]").
	if open := strings.Index(cleaned, "["); open >= 0 {
		if end := strings.LastIndex(cleaned, "]"); end > open {
			subset := cleaned[open : end+1]
			if items, err := decodeVoiceInventoryArray(subset); err == nil {
				return clampVoiceInventory(items), nil
			}
		}
	}

	// Fallback: direct decode — catches well-formed arrays that slip
	// past the slice heuristic.
	if items, err := decodeVoiceInventoryArray(cleaned); err == nil {
		return clampVoiceInventory(items), nil
	}

	return nil, fmt.Errorf("no se pudo interpretar la respuesta de la IA")
}

// stripMarkdownFences removes common code-fence wrappers around a
// JSON payload. Unlike stripMarkdownJSON in gemini_service.go (which
// is tuned for object responses and destructively trims at the last
// '}'), this helper only peels the fence markers + whitespace so it
// works for arrays too.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"```json", "```JSON", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func decodeVoiceInventoryArray(s string) ([]VoiceInventoryItem, error) {
	var out []VoiceInventoryItem
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func clampVoiceInventory(in []VoiceInventoryItem) []VoiceInventoryItem {
	out := make([]VoiceInventoryItem, 0, len(in))
	for _, item := range in {
		clean := VoiceInventoryItem{
			Name:     strings.TrimSpace(item.Name),
			Quantity: item.Quantity,
			Price:    item.Price,
		}
		if clean.Name == "" {
			// LLM hallucinated an empty entry — drop it rather than
			// surface an untitled row in the review UI.
			continue
		}
		if clean.Quantity < 0 {
			clean.Quantity = 0
		}
		if clean.Price < 0 {
			clean.Price = 0
		}
		out = append(out, clean)
	}
	return out
}
