// Spec: specs/098-aporte-automatico-catalogo/spec.md — Fase 2.
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
	"time"

	"vendia-backend/internal/models"
)

// verifyImageDownloadTimeout — presupuesto corto para descargar la foto que se
// va a verificar. Es un aporte fire-and-forget; nunca debe colgar el flujo.
const verifyImageDownloadTimeout = 10 * time.Second

// verifyImageMaxBytes — tope de descarga (defensa contra respuestas enormes).
const verifyImageMaxBytes = 10 << 20 // 10 MB

// catalogVerifyPromptFmt — prompt en español (temp 0) que le pide a Gemini
// confirmar si la imagen corresponde al producto. Un único %s = "<name>
// <presentation>". Salida EXACTA {"match":bool,"confidence":number}.
const catalogVerifyPromptFmt = `Usted verifica catálogos. ¿La imagen corresponde al producto llamado '%s'? Responda SOLO JSON {"match":bool,"confidence":number}. match=true solo si está claramente seguro de que la imagen muestra ese producto (o su empaque/marca). Ante duda, match=false.`

// VerifyImageMatchesProduct — le pregunta a Gemini si la imagen corresponde al
// producto (nombre + presentación). Devuelve true SOLO si el modelo confirma con
// alta confianza. Fail-safe: ante error/duda → false (no aporta). Spec 098.
func (s *GeminiService) VerifyImageMatchesProduct(ctx context.Context, imageURL, name, presentation string) (bool, error) {
	if s == nil {
		return false, nil
	}

	imageData, mimeType, ok := downloadImageForVerify(ctx, imageURL)
	if !ok {
		return false, nil
	}

	label := strings.TrimSpace(strings.TrimSpace(name) + " " + strings.TrimSpace(presentation))
	prompt := fmt.Sprintf(catalogVerifyPromptFmt, label)

	raw, err := s.callVerifyImage(ctx, imageData, mimeType, prompt)
	if err != nil {
		log.Printf("[CATALOG-VERIFY] gemini error (fail-safe false): %v", err)
		return false, nil
	}

	match, confidence := parseVerifyMatch(raw)
	return match && confidence >= 0.7, nil
}

// downloadImageForVerify baja la imagen con timeout corto. Devuelve
// (bytes, mime, ok). ok=false ante cualquier fallo — el caller trata eso como
// "no verificable" → no aporta.
func downloadImageForVerify(ctx context.Context, imageURL string) ([]byte, string, bool) {
	if strings.TrimSpace(imageURL) == "" {
		return nil, "", false
	}
	dlCtx, cancel := context.WithTimeout(ctx, verifyImageDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", false
	}
	req.Header.Set("User-Agent", "VendIA-POS/1.0 (vendia.store)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, verifyImageMaxBytes))
	if err != nil || len(data) == 0 {
		return nil, "", false
	}

	mime := resp.Header.Get("Content-Type")
	if idx := strings.IndexByte(mime, ';'); idx >= 0 {
		mime = mime[:idx]
	}
	mime = strings.TrimSpace(mime)
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/jpeg"
	}
	return data, mime, true
}

// callVerifyImage manda imagen + prompt a Gemini (modelo de texto/visión,
// temp 0, responseMimeType JSON) y devuelve el texto crudo de la respuesta.
func (s *GeminiService) callVerifyImage(ctx context.Context, imageData []byte, mimeType, prompt string) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(imageData)
	payload := map[string]any{
		"contents": []map[string]any{{
			"parts": []map[string]any{
				{"inlineData": map[string]any{"mimeType": mimeType, "data": b64}},
				{"text": prompt},
			},
		}},
		"generationConfig": map[string]any{
			"temperature":      0,
			"maxOutputTokens":  256,
			"responseMimeType": "application/json",
		},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		s.model, s.apiKey,
	)

	reqCtx, cancel := s.requestContext(ctx)
	defer cancel()

	req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini verify request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read gemini verify response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("gemini verify returned %d: %.200s", resp.StatusCode, respBody)
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse gemini verify envelope: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("gemini error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini verify response")
	}

	s.recordTokenUsage(ctx, models.AIFeatureCatalogVerify, s.model, &parsed)
	return strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text), nil
}

// parseVerifyMatch interpreta la respuesta cruda de Gemini de forma defensiva.
// Extraída como función pura para poder testear la validación sin red.
// Cualquier JSON ilegible → (false, 0) (fail-safe: no aporta).
func parseVerifyMatch(raw string) (bool, float64) {
	cleaned := stripMarkdownJSON(raw)
	var r struct {
		Match      bool    `json:"match"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(cleaned), &r); err != nil {
		return false, 0
	}
	if r.Confidence < 0 {
		r.Confidence = 0
	}
	if r.Confidence > 1 {
		r.Confidence = 1
	}
	return r.Match, r.Confidence
}
