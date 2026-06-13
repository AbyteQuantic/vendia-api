// Spec: specs/045-onboarding-agentic/onboarding_agentic_spec.md
package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"vendia-backend/internal/models"
)

// OnboardingFields is the set of fields the conversational onboarding can
// extract from a tendero's free text / voice. Every field is a pointer so
// "not mentioned" (nil) is distinct from "mentioned as empty/false" — the
// frontend merge is additive and nil never overwrites a manual edit (D7).
//
// NOTE (D10): PIN/contraseña and captcha are intentionally absent — the
// prompt orders the model to ignore them; sensitive data is typed apart.
type OnboardingFields struct {
	OwnerName           *string `json:"owner_name,omitempty"`
	OwnerLastName       *string `json:"owner_last_name,omitempty"`
	Phone               *string `json:"phone,omitempty"`
	BusinessName        *string `json:"business_name,omitempty"`
	RazonSocial         *string `json:"razon_social,omitempty"`
	NIT                 *string `json:"nit,omitempty"`
	Address             *string `json:"address,omitempty"`
	BusinessType        *string `json:"business_type,omitempty"`
	HasMultipleBranches *bool   `json:"has_multiple_branches,omitempty"`
	OffersServices      *bool   `json:"offers_services,omitempty"`
	SellsByWeight       *bool   `json:"sells_by_weight,omitempty"`
	HasTables           *bool   `json:"has_tables,omitempty"`
	LogoIntent          *string `json:"logo_intent,omitempty"`
	HasEmployees        *bool   `json:"has_employees,omitempty"`
}

// OnboardingModelOutput is exactly what the model is asked to return:
// detected fields + a per-field confidence 0..1 + an optional clarify prompt
// for the next turn. needs_confirmation is computed server-side (Go) from the
// confidence vs per-field thresholds — never trusted from the model (D8).
type OnboardingModelOutput struct {
	Fields        OnboardingFields   `json:"fields"`
	Confidence    map[string]float64 `json:"confidence"`
	ClarifyPrompt *string            `json:"clarify_prompt"`
}

// OnboardingParsePrompt is the strict, zero-shot extraction prompt. Exported
// so a test pins it against the Flutter mapping (test-drift guard), mirroring
// VoiceInventoryPrompt. Reglas clave: NO inventar (null > adivinar), IGNORAR
// el PIN, teléfono = 10 dígitos CO, mapear sinónimos coloquiales del tipo de
// negocio o null, confianza < umbral → null + clarify_prompt.
const OnboardingParsePrompt = `Eres un asistente que ayuda a un tendero colombiano a crear su negocio en una app. Tu ÚNICO trabajo es EXTRAER datos de lo que el usuario dice (texto o audio) y devolverlos en JSON. Hablas español.

DATOS QUE PUEDES EXTRAER (todos OPCIONALES — si no se mencionan, usa null):
- owner_name: SOLO el primer nombre del dueño.
- owner_last_name: SOLO los apellidos del dueño.
- phone: SOLO 10 dígitos del celular colombiano (quita +57, espacios, guiones y paréntesis). Si no hay 10 dígitos claros, null.
- business_name: el nombre del negocio.
- razon_social: razón social formal, solo si la dice explícitamente.
- nit: NIT, solo si lo dice explícitamente (solo dígitos).
- address: la dirección del negocio.
- business_type: UNO de esta lista EXACTA o null: tienda_barrio, minimercado, deposito_construccion, restaurante, comidas_rapidas, bar, manufactura, reparacion_muebles, emprendimiento_general, academias_instituciones.
- has_multiple_branches: true si dice que tiene varios locales/sucursales, false si dice que solo uno, null si no menciona.
- offers_services: true/false/null (si ofrece servicios además de productos).
- sells_by_weight: true/false/null (si vende por peso/gramos/libras).
- has_tables: true/false/null (si atiende en mesas).
- logo_intent: "generar" (quiere que la IA cree un logo), "subir" (tiene una imagen para subir), "omitir" (no quiere logo) o null.
- has_employees: true/false/null (si tiene empleados).

MAPEO de business_type (sinónimos coloquiales → enum, o null si no encaja):
- tienda/abarrotes/granero → tienda_barrio
- minimercado/autoservicio → minimercado
- ferretería/materiales/construcción → deposito_construccion
- restaurante/asadero/comedor → restaurante
- comidas rápidas/panadería/cafetería/heladería/pizzería → comidas_rapidas
- bar/licorera/estanco/cantina/discoteca → bar
- fábrica/taller de producción → manufactura
- mueblería/reparación de muebles/ebanistería → reparacion_muebles
- academia/colegio/instituto/gimnasio → academias_instituciones
- negocio propio/emprendimiento/varios → emprendimiento_general

REGLAS ESTRICTAS:
1. NO inventes datos. Si no estás seguro, usa null. Es MEJOR null que adivinar mal.
2. IGNORA por completo el PIN, la clave o la contraseña aunque el usuario los diga. NUNCA los incluyas.
3. Los booleanos son true SOLO si el usuario afirma, false SOLO si niega, null si no menciona.
4. Devuelve también "confidence": un objeto con la confianza 0.0–1.0 de CADA campo que detectaste (no incluyas los null).
5. Si algún dato importante quedó ambiguo o con baja confianza, escribe "clarify_prompt": una pregunta corta en español, trato de USTED, para aclararlo en el siguiente turno. Si no hace falta aclarar nada, usa null.
6. Responde ÚNICA Y EXCLUSIVAMENTE con UN objeto JSON válido. NADA de markdown, backticks, ni texto antes/después.

FORMATO EXACTO:
{"fields": {"owner_name": "string|null", "owner_last_name": "string|null", "phone": "string|null", "business_name": "string|null", "razon_social": "string|null", "nit": "string|null", "address": "string|null", "business_type": "enum|null", "has_multiple_branches": true/false/null, "offers_services": true/false/null, "sells_by_weight": true/false/null, "has_tables": true/false/null, "logo_intent": "generar|subir|omitir|null", "has_employees": true/false/null}, "confidence": {"campo": 0.0}, "clarify_prompt": "string|null"}`

// ExtractOnboardingFields sends the user's text and/or a voice note to Gemini
// multimodal with the strict prompt and returns the parsed model output. Like
// ExtractVoiceInventory it pins temperature=0 (no creativity) and leaves
// topP/topK at provider defaults (pinning them low starved audio transcription
// — documented 2026-04-24). `current` is the already-captured state (JSON) so
// the model does incremental extraction and does not re-emit known values (D7).
func (s *GeminiService) ExtractOnboardingFields(
	ctx context.Context,
	text string,
	audioData []byte,
	mimeType string,
	current string,
) (*OnboardingModelOutput, error) {
	if s == nil {
		return nil, fmt.Errorf("gemini service not configured")
	}

	parts := []map[string]any{}
	// Audio first (multimodal): a voice note is more robust for rural 50+
	// accents than relying on the device's STT (D13).
	if len(audioData) > 0 {
		if !IsSupportedAudioMimeType(mimeType) {
			return nil, fmt.Errorf("unsupported audio mime type: %s", mimeType)
		}
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{
				"mimeType": mimeType,
				"data":     base64.StdEncoding.EncodeToString(audioData),
			},
		})
	}

	userBlock := OnboardingParsePrompt
	if strings.TrimSpace(current) != "" {
		userBlock += "\n\nDATOS YA CAPTURADOS (no los repitas salvo que el usuario los corrija):\n" + current
	}
	if strings.TrimSpace(text) != "" {
		userBlock += "\n\nLO QUE DICE EL USUARIO:\n" + text
	}
	parts = append(parts, map[string]any{"text": userBlock})

	raw, err := s.callOnboardingParse(ctx, parts)
	if err != nil {
		return nil, err
	}

	cleaned := stripMarkdownFences(raw)
	// Tolerant slice: peel stray text around the object.
	if open := strings.Index(cleaned, "{"); open >= 0 {
		if end := strings.LastIndex(cleaned, "}"); end > open {
			cleaned = cleaned[open : end+1]
		}
	}
	var out OnboardingModelOutput
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, fmt.Errorf("no se pudo interpretar la respuesta de la IA: %w", err)
	}
	if out.Confidence == nil {
		out.Confidence = map[string]float64{}
	}
	return &out, nil
}

func (s *GeminiService) callOnboardingParse(
	ctx context.Context,
	parts []map[string]any,
) (string, error) {
	payload := map[string]any{
		"contents": []map[string]any{{"parts": parts}},
		"generationConfig": map[string]any{
			"temperature":      0,
			"maxOutputTokens":  768,
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
		return "", fmt.Errorf("gemini onboarding request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini onboarding response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("gemini onboarding returned %d: %.200s", resp.StatusCode, respBody)
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse gemini onboarding envelope: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini onboarding response")
	}

	s.recordTokenUsage(ctx, models.AIFeatureOnboarding, s.model, &parsed)
	return parsed.Candidates[0].Content.Parts[0].Text, nil
}

// SanitizeOnboardingFields applies the deterministic, server-side defenses the
// prompt must not be trusted to enforce (D9, phone normalization): it forces
// business_type into the whitelist (or nil) and logo_intent into its enum,
// and normalizes phone to digits. Pure function → unit-testable.
func SanitizeOnboardingFields(f *OnboardingFields) {
	if f == nil {
		return
	}
	if f.BusinessType != nil {
		t := strings.TrimSpace(*f.BusinessType)
		if _, ok := models.ValidBusinessTypes[t]; ok {
			f.BusinessType = &t
		} else {
			f.BusinessType = nil // defensa en profundidad: enum fuera de whitelist
		}
	}
	if f.LogoIntent != nil {
		li := strings.ToLower(strings.TrimSpace(*f.LogoIntent))
		if li == "generar" || li == "subir" || li == "omitir" {
			f.LogoIntent = &li
		} else {
			f.LogoIntent = nil
		}
	}
	if f.Phone != nil {
		digits := keepDigits(*f.Phone)
		// Colombian mobiles are 10 digits; keep the last 10 if a +57 slipped in.
		if len(digits) > 10 {
			digits = digits[len(digits)-10:]
		}
		if digits == "" {
			f.Phone = nil
		} else {
			f.Phone = &digits
		}
	}
}

func keepDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// OnboardingConfidenceThreshold returns the per-field confidence floor below
// which a detected value must NOT auto-fill and instead go to needs_confirmation
// (D8). business_type is strict (0.85) because it drives feature_flags; address
// is lenient (0.6); everything else 0.7.
func OnboardingConfidenceThreshold(field string) float64 {
	switch field {
	case "business_type":
		return 0.85
	case "address":
		return 0.6
	default:
		return 0.7
	}
}
