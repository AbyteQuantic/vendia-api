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
)

type GeminiService struct {
	apiKey     string
	model      string
	imageModel string
	timeout    time.Duration
}

func NewGeminiService(apiKey, model, imageModel string, timeout time.Duration) *GeminiService {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	svc := &GeminiService{apiKey: apiKey, model: model, imageModel: imageModel, timeout: timeout}

	// Discover models dynamically if not explicitly configured
	if model == "" || imageModel == "" {
		textModel, imgModel := svc.discoverModels()
		if model == "" {
			svc.model = textModel
		}
		if imageModel == "" {
			svc.imageModel = imgModel
		}
	}

	log.Printf("[GEMINI] Using models — text/OCR: %s | image: %s", svc.model, svc.imageModel)
	return svc
}

// discoverModels queries the Google AI API to find available models dynamically.
// Returns (textModel, imageModel) picking the best flash variants available.
func (s *GeminiService) discoverModels() (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", s.apiKey)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[GEMINI] Failed to list models: %v — using hardcoded fallbacks", err)
		return "gemini-2.0-flash", "gemini-2.5-flash-image"
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("[GEMINI] ListModels HTTP %d: %.200s — using fallbacks", resp.StatusCode, string(body))
		return "gemini-2.0-flash", "gemini-2.5-flash-image"
	}

	var listResp struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		log.Printf("[GEMINI] Failed to parse models list: %v", err)
		return "gemini-2.0-flash", "gemini-2.5-flash-image"
	}

	var textModel, imageModel string

	// Score models: prefer flash, then pro; prefer newer versions
	for _, m := range listResp.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		supportsGenerate := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				supportsGenerate = true
				break
			}
		}
		if !supportsGenerate {
			continue
		}

		isFlash := strings.Contains(name, "flash")
		isGemini := strings.Contains(name, "gemini")

		if !isGemini {
			continue
		}

		// Text/OCR model: prefer flash with multimodal capability
		if isFlash && !strings.Contains(name, "image") && !strings.Contains(name, "thinking") {
			if textModel == "" || strings.Compare(name, textModel) > 0 {
				textModel = name
			}
		}

		// Image generation model: prefer flash-image or flash-exp
		if strings.Contains(name, "image") || strings.Contains(name, "flash-exp") {
			if imageModel == "" || strings.Compare(name, imageModel) > 0 {
				imageModel = name
			}
		}
	}

	if textModel == "" {
		textModel = "gemini-2.0-flash"
	}
	if imageModel == "" {
		imageModel = "gemini-2.5-flash-image"
	}

	log.Printf("[GEMINI] Discovered %d models. Selected text=%s, image=%s", len(listResp.Models), textModel, imageModel)
	return textModel, imageModel
}

type InvoiceProduct struct {
	Name       string  `json:"name"`
	Quantity   int     `json:"quantity"`
	UnitPrice  float64 `json:"unit_price"`
	TotalPrice float64 `json:"total_price"`
	Barcode    string  `json:"barcode,omitempty"`
	// ExpiryDate is the best-before/expiration date printed next to the
	// line item, if any. Normalised to ISO-8601 (YYYY-MM-DD) by the
	// model. Empty when absent, unreadable, or uncertain.
	ExpiryDate string  `json:"expiry_date,omitempty"`
	Confidence float64 `json:"confidence"`
}

type InvoiceScanResult struct {
	Provider     string           `json:"provider"`
	Products     []InvoiceProduct `json:"products"`
	InvoiceTotal float64          `json:"invoice_total"`
}

func (s *GeminiService) ScanInvoice(ctx context.Context, imageData []byte, mimeType string) (*InvoiceScanResult, error) {
	prompt := `Eres un sistema OCR contable de PRECISIÓN ABSOLUTA. Tu única tarea es extraer los ítems facturados de esta imagen.

REGLAS CRÍTICAS (violarlas es inaceptable):
1. EXTRAE SOLO lo que está escrito TEXTUALMENTE en la tabla/lista de la factura.
2. PROHIBIDO inventar, deducir o suponer nombres de productos, marcas o cantidades que NO estén impresas en la imagen.
3. Si una fila está borrosa, cortada o no se puede leer con certeza, IGNÓRALA completamente. Prefiere menos productos correctos a más productos inventados.
4. El campo "name" debe contener el TEXTO EXACTO tal como aparece impreso en la factura (incluyendo abreviaciones como "PACA X12", "CJA", "UND").
5. Los precios deben ser los números EXACTOS impresos. No calcules ni redondees.
6. Si ves un nombre de proveedor en el encabezado, extráelo. Si no, pon "Desconocido".
7. El campo "confidence" debe reflejar tu certeza real (0.0 a 1.0). Si dudas de una lectura, pon confidence < 0.7.

REGLA DE FECHA DE VENCIMIENTO ("expiry_date"):
- Busca explícitamente etiquetas como "VENCE", "VENCIMIENTO", "CADUCIDAD", "FECHA VTO", "EXP", "BEST BEFORE", "CONSUMIR ANTES DE", "F.V.", "FV", "VTO" cerca de cada ítem.
- Si encuentras una fecha asociada a la línea, NORMALÍZALA a formato ISO "YYYY-MM-DD" sin importar cómo esté escrita en la factura (DD/MM/YYYY, MM-YY, etc.). Para formatos de solo mes/año (ej. "12/26"), asume el último día del mes (ej. "2026-12-31").
- Si NO hay fecha visible para esa línea, deja "expiry_date" como cadena vacía "". NO inventes fechas.
- Si hay una única fecha de vencimiento global para toda la factura (común en mayoristas), aplícala a TODAS las líneas.
- Si la fecha está borrosa o dudas de la lectura, deja "" — es preferible vacío que incorrecto.

Retorna JSON estricto sin markdown:
{"provider":"nombre del proveedor","products":[{"name":"texto exacto de factura","quantity":0,"unit_price":0,"total_price":0,"barcode":"","expiry_date":"YYYY-MM-DD","confidence":0.95}],"invoice_total":0}

Si la imagen NO es una factura o no contiene productos, retorna: {"provider":"","products":[],"invoice_total":0}

RETURN ONLY RAW JSON. DO NOT WRAP THE RESPONSE IN MARKDOWN BLOCK QUOTES. DO NOT ADD ANY EXPLANATIONS, COMMENTS OR TEXT OUTSIDE THE JSON.`

	text, err := s.callWithImageLowTemp(ctx, imageData, mimeType, prompt)
	if err != nil {
		return nil, err
	}

	// Log raw response for debugging
	log.Printf("[OCR] Raw AI response (%d chars): %.500s", len(text), text)

	// Strip markdown artifacts that break json.Unmarshal
	text = stripMarkdownJSON(text)

	if text == "" {
		return &InvoiceScanResult{Provider: "", Products: nil, InvoiceTotal: 0}, nil
	}

	var result InvoiceScanResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		log.Printf("[OCR] JSON parse error: %v | cleaned text: %.300s", err, text)
		return nil, fmt.Errorf("no se pudo interpretar la respuesta de la IA: %w", err)
	}
	return &result, nil
}

// stripMarkdownJSON removes markdown code fences and stray text around JSON.
func stripMarkdownJSON(s string) string {
	s = strings.TrimSpace(s)
	// Remove ```json ... ``` or ``` ... ```
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```JSON")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// If there's still stray text before the first '{', strip it
	if idx := strings.Index(s, "{"); idx > 0 {
		s = s[idx:]
	}
	// If there's stray text after the last '}', strip it
	if idx := strings.LastIndex(s, "}"); idx >= 0 && idx < len(s)-1 {
		s = s[:idx+1]
	}
	return strings.TrimSpace(s)
}

type LogoResult struct {
	ImageData []byte `json:"-"`
	MimeType  string `json:"mime_type"`
}

func (s *GeminiService) GenerateLogo(ctx context.Context, businessName, businessType string) ([]LogoResult, error) {
	prompt := fmt.Sprintf(
		`Un logo minimalista, profesional y moderno para un negocio llamado '%s'. `+
			`El tipo de negocio es '%s'. `+
			`Estilo de ilustración vectorial plana, diseño limpio, colores vibrantes, `+
			`fondo blanco puro sólido. Sin texto complejo adicional, centrado, `+
			`ideal para un avatar circular de aplicación móvil.`,
		businessName, businessType)

	results, err := s.callImageGeneration(ctx, prompt, 1)
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
   b) Centrar el producto completo en el encuadre con margen seguro.
   c) Aplicar iluminación suave y uniforme tipo estudio fotográfico.
5. Si el producto está recortado en los bordes, autocompleta el envase respetando la geometría y textura ORIGINAL.
6. Sin texto adicional, sin logos extras, sin marcas de agua.

ENCUADRE (REGLA CRÍTICA — no violar):
- Formato cuadrado 1:1.
- El producto NUNCA debe tocar los bordes de la imagen.
- El producto debe ocupar como máximo el 75%% del área de la imagen.
- Dejar un margen de "safe zone" BLANCO de al menos 12%% en los cuatro lados.
- Si la foto original está recortada o acercada, ALÉJATE: reduce la escala del producto hasta que entre completo con margen. No uses zoom/close-up.

Resultado esperado: Fotografía tipo catálogo Amazon — producto REAL sobre fondo blanco puro, centrado con aire alrededor.`, description)

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

// ReferenceImage is a product photo from the tenant's catalogue that
// Gemini should use as visual anchor when composing the banner —
// instead of hallucinating a generic image of "empanada". The model
// is told in the prompt that these are the REAL products to render.
type ReferenceImage struct {
	MimeType string // "image/jpeg", "image/png", "image/webp"
	Data     []byte // raw bytes (we base64-encode internally)
}

// GeneratePromoBanner produces a retail-advertising banner image
// (horizontal 16:9 — the aspect ratio the web catalogue's "Special
// Offers" carousel expects) from a fully-formed prompt. The caller is
// responsible for prompt assembly — this function just drives the
// Gemini image model and returns the decoded PNG/JPEG bytes.
//
// Rationale for a dedicated method (vs reusing GenerateProductImage):
// banners have different generation params — higher guidance, no
// "product isolated on white" safeguards, room for embedded copy —
// and tracing the two use cases separately in logs is a must.
//
// refImages (may be nil/empty): product photos from the tenant's own
// catalogue. When present, the request becomes multimodal: Gemini
// receives the images as inlineData parts BEFORE the prompt text,
// which gives it a concrete visual anchor for each product instead
// of generating a generic render. Empty → text-only generation.
func (s *GeminiService) GeneratePromoBanner(ctx context.Context, prompt string, refImages []ReferenceImage) ([]byte, error) {
	parts := make([]map[string]any, 0, len(refImages)+1)
	for _, img := range refImages {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MimeType
		if mime == "" {
			mime = "image/jpeg"
		}
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{
				"mimeType": mime,
				"data":     base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	parts = append(parts, map[string]any{"text": prompt})

	payload := map[string]any{
		"contents": []map[string]any{
			{"parts": parts},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"TEXT", "IMAGE"},
			// Mid-range temperature: we want creative composition but
			// consistent typography. Higher values produced unreadable
			// text in pilots; lower values produced repetitive layouts.
			"temperature": 0.55,
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
		return nil, fmt.Errorf("gemini banner request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read banner response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse banner response: %w", err)
	}

	if geminiResp.Error != nil {
		return nil, fmt.Errorf("gemini error %d: %s", geminiResp.Error.Code, geminiResp.Error.Message)
	}

	for _, candidate := range geminiResp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData.Data == "" {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to decode banner image: %w", err)
			}
			if len(decoded) < 1024 {
				return nil, fmt.Errorf("banner image suspiciously small (%d bytes)", len(decoded))
			}
			return decoded, nil
		}
	}

	return nil, fmt.Errorf("no image returned from Gemini for promo banner")
}

// GenerateProductImage creates a product image from just a text description (no source photo needed).
func (s *GeminiService) GenerateProductImage(ctx context.Context, productInfo string) ([]byte, error) {
	prompt := fmt.Sprintf(`Genera una foto profesional de e-commerce del siguiente producto: %s

ENCUADRE (REGLA CRÍTICA — no violar):
- Formato cuadrado 1:1.
- El producto NUNCA debe tocar los bordes de la imagen.
- El producto debe ocupar como máximo el 75%% del área de la imagen.
- Dejar un margen de "safe zone" BLANCO de al menos 12%% en los cuatro lados.
- Centrar el producto horizontal y verticalmente dentro de ese margen.
- Prohibido acercamientos (close-up) que recorten el producto o hagan que roce los bordes.

Estilo fotográfico:
- Fondo BLANCO puro (#FFFFFF), limpio, sin sombras de fondo.
- Producto completo y entero visible, sin recortar.
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
				if len(decoded) < 100 {
					return nil, fmt.Errorf("generated image too small (%d bytes), likely corrupted", len(decoded))
				}
				return decoded, nil
			}
		}
	}

	return nil, fmt.Errorf("no image returned from Gemini for product generation")
}

// callWithImageLowTemp is like callWithImage but forces temperature=0 for strict OCR extraction.
func (s *GeminiService) callWithImageLowTemp(ctx context.Context, imageData []byte, mimeType, prompt string) (string, error) {
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
			"temperature":      0.0,
		},
	}

	body, _ := json.Marshal(payload)

	// Use the dynamically discovered model (set at init time)
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

	if resp.StatusCode != http.StatusOK {
		log.Printf("[GEMINI-OCR] Model %s returned HTTP %d: %.300s", s.model, resp.StatusCode, string(respBody))
		return "", fmt.Errorf("gemini API returned HTTP %d for model %s", resp.StatusCode, s.model)
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		log.Printf("[GEMINI-OCR] Parse error: %.500s", string(respBody))
		return "", fmt.Errorf("failed to parse gemini response: %w", err)
	}

	if geminiResp.Error != nil {
		return "", fmt.Errorf("gemini error %d: %s", geminiResp.Error.Code, geminiResp.Error.Message)
	}

	if len(geminiResp.Candidates) == 0 {
		log.Printf("[GEMINI-OCR] No candidates: %.500s", string(respBody))
		return "", fmt.Errorf("la IA no generó respuesta. Intente con otra foto")
	}

	if len(geminiResp.Candidates[0].Content.Parts) == 0 {
		log.Printf("[GEMINI-OCR] No parts: %.500s", string(respBody))
		return "", fmt.Errorf("respuesta vacía de la IA")
	}

	text := geminiResp.Candidates[0].Content.Parts[0].Text
	return strings.TrimSpace(text), nil
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
