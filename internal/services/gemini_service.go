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
	"time"
)

type GeminiService struct {
	apiKey  string
	model   string
	timeout time.Duration
}

func NewGeminiService(apiKey, model string, timeout time.Duration) *GeminiService {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &GeminiService{apiKey: apiKey, model: model, timeout: timeout}
}

type InvoiceProduct struct {
	Name       string  `json:"name"`
	Quantity   int     `json:"quantity"`
	UnitPrice  float64 `json:"unit_price"`
	TotalPrice float64 `json:"total_price"`
	Barcode    string  `json:"barcode,omitempty"`
	Confidence float64 `json:"confidence"`
}

type InvoiceScanResult struct {
	Provider     string           `json:"provider"`
	Products     []InvoiceProduct `json:"products"`
	InvoiceTotal float64          `json:"invoice_total"`
}

func (s *GeminiService) ScanInvoice(ctx context.Context, imageData []byte, mimeType string) (*InvoiceScanResult, error) {
	prompt := `Analiza esta imagen de factura de proveedor colombiano.
Extrae TODOS los productos con: nombre, cantidad, precio_unitario, precio_total.
Si ves códigos de barras, inclúyelos.
Retorna JSON estricto sin markdown con esta estructura:
{"provider":"nombre proveedor","products":[{"name":"","quantity":0,"unit_price":0,"total_price":0,"barcode":"","confidence":0.95}],"invoice_total":0}`

	text, err := s.callWithImage(ctx, imageData, mimeType, prompt)
	if err != nil {
		return nil, err
	}

	var result InvoiceScanResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini invoice response: %w (raw: %s)", err, text)
	}
	return &result, nil
}

type LogoResult struct {
	ImageData []byte `json:"-"`
	MimeType  string `json:"mime_type"`
}

func (s *GeminiService) GenerateLogo(ctx context.Context, businessName, businessType string) ([]LogoResult, error) {
	prompt := fmt.Sprintf(
		`Genera un logo profesional y moderno para un negocio colombiano llamado '%s'.
Tipo de negocio: %s.
Estilo: minimalista, fondo sólido de color vibrante, iniciales grandes o ícono simple.
Formato: cuadrado 512x512, esquinas redondeadas.`, businessName, businessType)

	results, err := s.callImageGeneration(ctx, prompt, 3)
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *GeminiService) EnhancePhoto(ctx context.Context, imageData []byte, mimeType string) ([]byte, error) {
	prompt := `Mejora esta foto de comida para un catálogo de restaurante.
Mantén la comida exactamente igual (no alterar el plato).
Cambia el fondo a negro elegante profesional.
Mejora iluminación y colores. Estilo food photography premium.`

	b64 := base64.StdEncoding.EncodeToString(imageData)

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
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"TEXT", "IMAGE"},
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.model, s.apiKey)

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, _ := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini enhance request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read enhance response: %w", err)
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse enhance response: %w", err)
	}

	for _, candidate := range geminiResp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData.Data != "" {
				decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode enhanced image: %w", err)
				}
				return decoded, nil
			}
		}
	}

	return nil, fmt.Errorf("no image returned from Gemini enhance")
}

func (s *GeminiService) callWithImage(ctx context.Context, imageData []byte, mimeType, prompt string) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(imageData)

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
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.model, s.apiKey)

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, _ := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini response: %w", err)
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return "", fmt.Errorf("failed to parse gemini response: %w", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini response")
	}

	text := geminiResp.Candidates[0].Content.Parts[0].Text
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	return text, nil
}

func (s *GeminiService) callImageGeneration(ctx context.Context, prompt string, count int) ([]LogoResult, error) {
	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"IMAGE"},
			"candidateCount":     count,
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.model, s.apiKey)

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, _ := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini image gen request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read image gen response: %w", err)
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse image gen response: %w", err)
	}

	var results []LogoResult
	for _, candidate := range geminiResp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData.Data != "" {
				decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil {
					continue
				}
				results = append(results, LogoResult{
					ImageData: decoded,
					MimeType:  part.InlineData.MimeType,
				})
			}
		}
	}

	return results, nil
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text       string `json:"text"`
				InlineData struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}
