package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
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
// Hardening (2026-04-24): after production users reported the model
// inventing Colombian retail brands ("Arroz Roa", "Aceite Premier",
// "Huevos AAA") when the audio is unclear, we rewrote the prompt as
// strict zero-shot — no example rows, "echo the user verbatim",
// and an explicit "return []" escape hatch. Combined with
// temperature=0 in the request payload this eliminates the
// hallucination path: the model either transcribes or returns empty.
const VoiceInventoryPrompt = `Eres un procesador de datos estricto para un inventario. Tu ÚNICO trabajo es extraer los productos mencionados en el texto del usuario y convertirlos a JSON.

REGLAS ESTRICTAS:
1. NO inventes productos. Usa EXACTAMENTE las palabras que dice el usuario en el audio, palabra por palabra.
2. Si el audio es incomprensible, está vacío, o no menciona productos claros, DEBES retornar un arreglo vacío: []
3. NUNCA uses ejemplos predeterminados ni productos genéricos cuando no entiendas. Mejor retorna [].
4. NO inventes marcas ni presentaciones que el usuario no dijo. Preserva tal cual lo que oíste.
5. El precio es 0 si el usuario no lo menciona. La cantidad es 1 si no la menciona.
6. Responde ÚNICA Y EXCLUSIVAMENTE con un JSON Array válido. NO uses bloques de código markdown. NO uses backticks. NO escribas la palabra "json" antes del arreglo. NO agregues texto antes ni después.
7. Formato obligatorio: [{"name": "string", "quantity": int, "price": float}]`

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

// ExtractVoiceInventory sends the raw audio to Gemini multimodal
// with the strict zero-shot prompt (see VoiceInventoryPrompt). Uses
// its own HTTP path (instead of sharing callWithImage with OCR) so
// it can pin temperature=0 — a 2026-04-24 hallucination incident
// had the default temperature let Gemini invent Colombian retail
// brands when the audio was unclear. The request is now:
//
//   - temperature: 0            (no creativity)
//   - topP: 0.1 / topK: 1       (narrow sampling window)
//   - responseMimeType: json    (structured output)
//   - maxOutputTokens: 512      (fail-closed cap)
//
// The raw response is logged (truncated) on every call so the next
// hallucination report has a full audit trail without redeploying
// with debug flags.
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

	raw, err := s.callVoiceInventory(ctx, audioData, mimeType)
	if err != nil {
		return nil, err
	}

	// Always log the raw Gemini response (truncated). Future
	// hallucination reports need this trail — without it we have no
	// way to tell "bad audio" apart from "model went off-script".
	log.Printf("[VOICE] gemini raw response (%d bytes): %.400s",
		len(raw), raw)

	items, err := ParseVoiceInventoryJSON(raw)
	if err != nil {
		log.Printf("[VOICE] parse error: %v | raw=%.300s", err, raw)
		return nil, err
	}
	return items, nil
}

// callVoiceInventory is the dedicated Gemini call for the voice
// inventory flow. Kept inline here (instead of GeminiService's
// generic callWithImage) so the strict generationConfig below
// doesn't leak into OCR / banner / photo-enhance paths where some
// creativity is intentional.
func (s *GeminiService) callVoiceInventory(
	ctx context.Context,
	audioData []byte,
	mimeType string,
) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(audioData)

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{
						"inlineData": map[string]any{
							"mimeType": mimeType,
							"data":     b64,
						},
					},
					{"text": VoiceInventoryPrompt},
				},
			},
		},
		"generationConfig": map[string]any{
			// temperature=0 eliminates the "plausible completion"
			// path that produced the 2026-04-24 hallucination
			// ("Arroz Roa", "Aceite Premier", "Huevos AAA"). We
			// intentionally leave topP / topK at the provider
			// defaults — pinning them both too low (topK=1,
			// topP=0.1) starves the decoder during transcription
			// and made Gemini emit "[]" even for clean audio that
			// said "30 cervezas aguila 350". Temperature alone is
			// enough to lock out the creativity anchor; the strict
			// prompt handles the rest.
			"temperature":      0,
			"maxOutputTokens":  512,
			"responseMimeType": "application/json",
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		s.model, s.apiKey,
	)

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini voice request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini voice response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf(
			"gemini voice returned %d: %.200s",
			resp.StatusCode, respBody,
		)
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse gemini voice envelope: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini voice response")
	}

	text := parsed.Candidates[0].Content.Parts[0].Text
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text), nil
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
