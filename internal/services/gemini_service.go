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
	apiKey     string
	model      string
	imageModel string
	timeout    time.Duration
}

func NewGeminiService(apiKey, model, imageModel string, timeout time.Duration) *GeminiService {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	if imageModel == "" {
		imageModel = "gemini-2.5-flash-image"
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &GeminiService{apiKey: apiKey, model: model, imageModel: imageModel, timeout: timeout}
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

// EnhancePhoto generates a professional e-commerce product photo.
// productInfo is optional context (e.g., "Coca-Cola Botella 350ml").
func (s *GeminiService) EnhancePhoto(ctx context.Context, imageData []byte, mimeType string, productInfo string) ([]byte, error) {
	description := ""
	if productInfo != "" {
		description = fmt.Sprintf("\nEl producto es: %s.", productInfo)
	}

	prompt := fmt.Sprintf(`Eres un EDITOR FOTOGRÁFICO profesional, NO un artista creativo.%s

TAREA: Toma esta foto real de un producto y edítala para catálogo de e-commerce.

REGLAS ESTRICTAS (PROHIBIDO violarlas):
1. PROHIBIDO cambiar el color original del producto. Si es rojo, DEBE seguir siendo rojo. Si es verde, DEBE seguir siendo verde. Los colores originales son SAGRADOS.
2. PROHIBIDO alterar las letras, marca, logo o forma del envase/empaque.
3. PROHIBIDO inventar detalles que no existan en la foto original.
4. Tu ÚNICA función es:
   a) Eliminar el fondo y reemplazarlo por BLANCO PURO sólido (#FFFFFF).
   b) Centrar el producto completo en el encuadre.
   c) Aplicar iluminación suave y uniforme tipo estudio fotográfico.
5. Si el producto está recortado en los bordes, autocompleta el envase respetando la geometría y textura ORIGINAL.
6. Sin texto adicional, sin logos extras, sin marcas de agua.

Resultado esperado: Fotografía tipo catálogo Amazon — producto REAL sobre fondo blanco puro.`, description)

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
			"temperature":        0.2, // Low creativity — preserve original colors
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.imageModel, s.apiKey)

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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse enhance response: %w", err)
	}

	if geminiResp.Error != nil {
		return nil, fmt.Errorf("gemini error %d: %s", geminiResp.Error.Code, geminiResp.Error.Message)
	}

	// Collect any text response for debugging
	var textParts []string
	for _, candidate := range geminiResp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData.Data != "" {
				decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode enhanced image: %w", err)
				}
				return decoded, nil
			}
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
	}

	if len(textParts) > 0 {
		return nil, fmt.Errorf("gemini returned text instead of image: %s", strings.Join(textParts, " ")[:min(len(strings.Join(textParts, " ")), 200)])
	}

	return nil, fmt.Errorf("no image returned from Gemini (candidates=%d)", len(geminiResp.Candidates))
}

// GenerateProductImage creates a product image from just a text description (no source photo needed).
func (s *GeminiService) GenerateProductImage(ctx context.Context, productInfo string) ([]byte, error) {
	prompt := fmt.Sprintf(`Genera una foto profesional de e-commerce del siguiente producto: %s

Instrucciones estrictas:
- Fondo BLANCO puro (#FFFFFF), limpio, sin sombras de fondo.
- Mostrar el producto completo, centrado, sin recortar.
- Iluminación suave y uniforme tipo estudio fotográfico.
- Colores fieles al producto real, nítidos y vibrantes.
- Sin texto, logos adicionales, ni marcas de agua.
- Estilo Amazon/MercadoLibre: producto aislado sobre fondo blanco.
- La imagen debe ser digna de un catálogo de e-commerce profesional.
- El producto debe verse realista, como una foto real del producto.`, productInfo)

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"TEXT", "IMAGE"},
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.imageModel, s.apiKey)

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, _ := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini generate image request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read generate image response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse generate image response: %w", err)
	}

	if geminiResp.Error != nil {
		return nil, fmt.Errorf("gemini error %d: %s", geminiResp.Error.Code, geminiResp.Error.Message)
	}

	for _, candidate := range geminiResp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData.Data != "" {
				decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode generated image: %w", err)
				}
				return decoded, nil
			}
		}
	}

	return nil, fmt.Errorf("no image returned from Gemini for product generation")
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
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.imageModel, s.apiKey)

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
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}
