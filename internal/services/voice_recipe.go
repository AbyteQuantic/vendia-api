// Spec: specs/065-recipe-studio/spec.md
package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"vendia-backend/internal/models"
)

// VoiceRecipeIngredient is one parsed ingredient line from a dictated recipe.
type VoiceRecipeIngredient struct {
	Name     string  `json:"name"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
}

// VoiceRecipeResult is the structured recipe the assistant returns from either
// dictated audio (ExtractVoiceRecipe) or a text draft/refine (GenerateRecipeDraft).
// The frontend opens the Recipe Studio prefilled with this — the user always
// reviews/edits before saving (never published blindly).
type VoiceRecipeResult struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Yield       string                  `json:"yield"`
	PrepTime    string                  `json:"prep_time"`
	Ingredients []VoiceRecipeIngredient `json:"ingredients"`
	Steps       []string                `json:"steps"`
}

// VoiceRecipePrompt instructs Gemini to transcribe a spoken recipe in Colombian
// Spanish into structured JSON. temperature=0 (set in the request) keeps it from
// inventing ingredients (same hallucination guard as voice inventory).
const VoiceRecipePrompt = `Eres un asistente de cocina. Vas a ESCUCHAR a un tendero/cocinero colombiano que dicta una receta de un plato.
Devuelve SOLO un objeto JSON (sin texto extra, sin markdown) con esta forma EXACTA:
{
  "name": "nombre del plato",
  "description": "descripción corta y apetitosa (opcional, puede ir vacía)",
  "yield": "rendimiento si lo menciona, p. ej. '10 porciones' (opcional)",
  "prep_time": "tiempo si lo menciona, p. ej. '30 min' (opcional)",
  "ingredients": [ { "name": "ingrediente", "quantity": 2, "unit": "unidades" } ],
  "steps": [ "paso 1", "paso 2" ]
}
Reglas:
- Transcribe SOLO lo que se dice. NO inventes ingredientes, marcas ni pasos.
- Si no menciona cantidad, usa 1. Si no menciona unidad, usa "" (vacío).
- Español neutro colombiano. No uses voseo.
- Si el audio no contiene una receta, devuelve {"name":"","ingredients":[],"steps":[]}.`

// ExtractVoiceRecipe ships dictated audio to Gemini and returns the structured
// recipe. Mirrors ExtractVoiceInventory (same audio plumbing + guards).
func (s *GeminiService) ExtractVoiceRecipe(
	ctx context.Context,
	audioData []byte,
	mimeType string,
) (VoiceRecipeResult, error) {
	if s == nil {
		return VoiceRecipeResult{}, fmt.Errorf("gemini service not configured")
	}
	if !IsSupportedAudioMimeType(mimeType) {
		return VoiceRecipeResult{}, fmt.Errorf("unsupported audio mime type: %s", mimeType)
	}

	raw, err := s.callVoiceRecipe(ctx, audioData, mimeType)
	if err != nil {
		return VoiceRecipeResult{}, err
	}
	log.Printf("[VOICE_RECIPE] gemini raw response (%d bytes): %.400s", len(raw), raw)
	return ParseVoiceRecipeJSON(raw)
}

// GenerateRecipeDraft is the TEXT assistant: completar/refinar. Given a dish
// name, the current draft and optional free-text instructions ("hazla más
// económica", "para 10 porciones", "sin lácteos"), it returns a refined recipe.
// Reuses the JSON parser so the contract matches the voice path.
func (s *GeminiService) GenerateRecipeDraft(
	ctx context.Context,
	name, instructions string,
	current VoiceRecipeResult,
) (VoiceRecipeResult, error) {
	if s == nil {
		return VoiceRecipeResult{}, fmt.Errorf("gemini service not configured")
	}
	currentJSON, _ := json.Marshal(current)
	prompt := fmt.Sprintf(`Eres un asistente de cocina para un tendero colombiano. Ayuda a COMPLETAR o REFINAR una receta.
Plato: %q
Borrador actual (JSON, puede estar incompleto o vacío): %s
Instrucciones del usuario (puede estar vacío): %q

Devuelve SOLO un objeto JSON (sin markdown, sin texto extra) con la forma EXACTA:
{"name":"...","description":"...","yield":"...","prep_time":"...","ingredients":[{"name":"...","quantity":1,"unit":"..."}],"steps":["..."]}
Reglas:
- Respeta el plato indicado y el borrador; aplica las instrucciones del usuario.
- Propón ingredientes y pasos realistas y caseros (cocina colombiana). Cantidades razonables.
- Si no hay unidad clara usa "" y cantidad 1.
- Español neutro colombiano, sin voseo. NO inventes marcas comerciales.`,
		name, string(currentJSON), instructions)

	raw, err := s.GenerateText(ctx, prompt)
	if err != nil {
		return VoiceRecipeResult{}, err
	}
	s.recordFeature(models.AIFeatureRecipeAssist)
	log.Printf("[RECIPE_ASSIST] gemini raw response (%d bytes): %.400s", len(raw), raw)
	return ParseVoiceRecipeJSON(raw)
}

// recordFeature is a no-op hook placeholder kept tiny: GenerateText already
// records token usage under CHAT_IA; we only log the feature label for traceability.
func (s *GeminiService) recordFeature(feature string) {
	log.Printf("[AI_FEATURE] %s", feature)
}

func (s *GeminiService) callVoiceRecipe(
	ctx context.Context,
	audioData []byte,
	mimeType string,
) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(audioData)

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"inlineData": map[string]any{"mimeType": mimeType, "data": b64}},
					{"text": VoiceRecipePrompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"temperature":      0,
			"maxOutputTokens":  1024,
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
		return "", fmt.Errorf("gemini voice-recipe request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini voice-recipe response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("gemini voice-recipe returned %d: %.200s", resp.StatusCode, respBody)
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse gemini voice-recipe envelope: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini voice-recipe response")
	}

	s.recordTokenUsage(ctx, models.AIFeatureVoiceRecipe, s.model, &parsed)

	text := parsed.Candidates[0].Content.Parts[0].Text
	return strings.TrimSpace(stripMarkdownFences(text)), nil
}

// ParseVoiceRecipeJSON decodes the assistant's JSON object into a
// VoiceRecipeResult. Tolerant of markdown fences and stray text; clamps
// negative quantities to 0. Pure function — exported for tests.
func ParseVoiceRecipeJSON(raw string) (VoiceRecipeResult, error) {
	cleaned := stripMarkdownFences(raw)
	// Strip to the outermost object braces if there's stray text.
	if i := strings.IndexByte(cleaned, '{'); i > 0 {
		cleaned = cleaned[i:]
	}
	if j := strings.LastIndexByte(cleaned, '}'); j >= 0 && j < len(cleaned)-1 {
		cleaned = cleaned[:j+1]
	}
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return VoiceRecipeResult{Ingredients: []VoiceRecipeIngredient{}, Steps: []string{}}, nil
	}

	var out VoiceRecipeResult
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return VoiceRecipeResult{}, fmt.Errorf("voice-recipe parse error: %w", err)
	}

	// Clamp + clean.
	out.Name = strings.TrimSpace(out.Name)
	cleanIng := make([]VoiceRecipeIngredient, 0, len(out.Ingredients))
	for _, ing := range out.Ingredients {
		name := strings.TrimSpace(ing.Name)
		if name == "" {
			continue
		}
		q := ing.Quantity
		if q < 0 {
			q = 0
		}
		cleanIng = append(cleanIng, VoiceRecipeIngredient{
			Name: name, Quantity: q, Unit: strings.TrimSpace(ing.Unit),
		})
	}
	out.Ingredients = cleanIng

	cleanSteps := make([]string, 0, len(out.Steps))
	for _, st := range out.Steps {
		if s := strings.TrimSpace(st); s != "" {
			cleanSteps = append(cleanSteps, s)
		}
	}
	out.Steps = cleanSteps
	return out, nil
}
