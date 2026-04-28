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
	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

type GeminiService struct {
	apiKey     string
	model      string
	imageModel string
	timeout    time.Duration
	usageDB    *gorm.DB
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

// WithUsageDB wires PostgreSQL for persisting token usage to ai_usage_logs
// (FinOps). Safe to call with nil; logging is a no-op when unset.
func (s *GeminiService) WithUsageDB(db *gorm.DB) *GeminiService {
	if s == nil {
		return s
	}
	s.usageDB = db
	return s
}

// recordTokenUsage runs after a successful Google API JSON parse. Cost uses
// EstimateGeminiCostUSD; tenant comes from aiusage.WithTenantID in handlers.
func (s *GeminiService) recordTokenUsage(ctx context.Context, feature, modelName string, gr *geminiResponse) {
	if s == nil || s.usageDB == nil || gr == nil {
		return
	}
	tid := aiusage.TenantIDFromContext(ctx)
	if tid == "" {
		return
	}
	in := gr.UsageMetadata.PromptTokenCount
	out := gr.UsageMetadata.CandidatesTokenCount
	if in == 0 && out == 0 && gr.UsageMetadata.TotalTokenCount > 0 {
		out = gr.UsageMetadata.TotalTokenCount
	}
	if in == 0 && out == 0 {
		return
	}
	cost := EstimateGeminiCostUSD(modelName, in, out)
	row := models.AIUsageLog{
		TenantID:         tid,
		Feature:          feature,
		TokensInput:      int64(in),
		TokensOutput:     int64(out),
		EstimatedCostUSD: cost,
		ModelName:        modelName,
	}
	if err := s.usageDB.Create(&row).Error; err != nil {
		log.Printf("[AI-USAGE] insert failed: %v", err)
	}
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

// GenerateLogo asks Gemini for a single brand mark for a small
// Colombian business. The prompt is intentionally prescriptive — the
// model is good at rendering shapes and bad at rendering text + 3D
// effects, so we steer it hard toward flat vector iconography that
// works as a 24px app icon AND a 512px hero on the public catalog.
//
// The output lands in `store-logos`/Cloudflare R2 (handler side) and
// must therefore be square, on a solid background, with generous
// safe-area padding so circular cropping by a downstream UI never
// clips the subject.
func (s *GeminiService) GenerateLogo(
	ctx context.Context,
	businessName, businessType, details string,
) ([]LogoResult, error) {
	industryHint := industryIconHint(businessType)
	typeLabel := businessTypeLabel(businessType)

	// Brand-tone line. When the merchant wrote a description in the
	// onboarding logo step ("vendo helados artesanales con sabores de
	// frutas") we fold it in verbatim so the model picks symbology /
	// palette accents matching what they actually sell. Empty string
	// → omit the line entirely so the rubro hint stays the dominant
	// signal.
	detailsLine := ""
	details = strings.TrimSpace(details)
	if details != "" {
		// Cap at 240 chars — same as the client UI — so a malicious
		// caller can't pad the prompt with thousands of tokens.
		if len(details) > 240 {
			details = details[:240]
		}
		detailsLine = fmt.Sprintf(`
- Owner's brand tone (verbatim — fold into the symbol choice and palette accents): "%s"`,
			details)
	}

	prompt := fmt.Sprintf(`Generate a single, professional, BRAND LOGO for a small business in Colombia. Output: a 1024x1024 square image, suitable as a mobile app icon, WhatsApp avatar, and public-catalog hero.

BUSINESS CONTEXT
- Brand name: "%s"
- Industry: %s%s
- Audience: adults 50+ running an informal neighbourhood business in Latin America. The logo must read as TRUSTED and HONEST, not flashy or trendy.

UI/UX DESIGN REQUIREMENTS (mandatory — violations are unacceptable)
1. STYLE: Flat vector illustration. Geometric, modern, minimalist. NO photorealism, NO 3D, NO gradients, NO drop shadows, NO film grain, NO sketchy lines.
2. COLOR PALETTE: 2 to 3 colours maximum (subject + accent + background). Bold and saturated, NOT pastel. Pick a warm, professional combination — examples that work: deep indigo + warm cream, terracotta + ivory, charcoal + mustard, sage green + bone, navy + amber. If the owner mentioned a colour preference in the brand-tone line above, weight the palette toward it.
3. BACKGROUND: SOLID single colour or pure white (#FFFFFF). NEVER transparent, NEVER gradient, NEVER patterned, NEVER textured paper.
4. COMPOSITION: Subject perfectly centred. Reserve 10-15%% safe-area padding on all four sides so a circular crop never amputates the subject. Balance positive and negative space.
5. NO TEXT WHATSOEVER. No letters, no words, no logograms, no monograms, no decorative typography. The model renders text poorly and any garbled glyph would destroy the brand. The brand name appears alongside the logo in the app — the logo itself is purely a symbol.
6. SUBJECT: A SINGLE, recognisable iconographic mark. %s If the brand-tone line mentions a specific product (e.g. "helados", "ropa de niños", "panadería con horno de leña"), bias the symbol toward that product over the generic industry icon — that's what makes THIS business different.
7. SCALABILITY: Must remain instantly recognisable at 24px (app icon size) AND impressive at 512px (catalog header). Avoid any detail finer than 1/40th of the canvas.
8. STROKE WEIGHTS: Consistent. If using outlines, all strokes should share one of at most two thicknesses.

OUTPUT
Return ONLY the image. No watermarks, no text overlays, no signatures, no annotations, no border frames.`,
		businessName, typeLabel, detailsLine, industryHint)

	results, err := s.callImageGeneration(ctx, models.AIFeatureLogoGen, prompt, 1)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// industryIconHint maps the backend business-type enum to a short
// English brief steering the model toward an industry-appropriate
// symbol. Listed as alternatives so the model can pick whichever
// composes best — single deterministic symbols tend to feel sterile.
func industryIconHint(businessType string) string {
	switch businessType {
	case "tienda_barrio":
		return `Suggested symbols: a stylised storefront facade with awning, a paper grocery bag, a wicker basket of staples, or a small house silhouette with a window. Evokes "the corner store the whole neighbourhood trusts".`
	case "minimercado":
		return `Suggested symbols: a shopping cart with a few groceries, a stack of fresh produce, or a market stall icon. Evokes "fresh, organised, well-stocked".`
	case "restaurante":
		return `Suggested symbols: a crossed fork and knife, a covered plate with steam wisps, a chef's hat, or an open menu. Evokes "warm hospitality, good food".`
	case "comidas_rapidas":
		return `Suggested symbols: a burger silhouette, a paper bag of fries, a lightning bolt, or a take-away cup. Evokes "quick, satisfying, fun".`
	case "bar":
		return `Suggested symbols: a cocktail glass, a beer mug silhouette, a wine bottle and glass pair, or a neon star. Evokes "vibrant nightlife, social".`
	case "deposito_construccion":
		return `Suggested symbols: a hard hat, a toolbox, a ruler and pencil crossed, or a stack of bricks. Evokes "reliable, builders' supply".`
	case "manufactura":
		return `Suggested symbols: a single gear, a stylised factory roofline, or a stack of geometric shapes. Evokes "production, precision".`
	case "reparacion_muebles":
		return `Suggested symbols: a wrench, a screwdriver and hammer crossed, a chair silhouette, or a sewing-needle-and-thread for upholstery. Evokes "skilled hands, restoration".`
	case "emprendimiento_general":
		return `Suggested symbols: a rocket silhouette, a lightbulb, a mountain peak with sun, or a rising graph arrow. Evokes "ambition, growth, ideas".`
	default:
		return `Use a clean abstract geometric mark — circle + square or a stylised monogram silhouette — that conveys "professional small business".`
	}
}

// businessTypeLabel turns the backend enum into a human label the
// model can reason about (it's still in Spanish — the model speaks
// every language in the prompt window equally well, but a translated
// label primes industry semantics better than the raw enum).
func businessTypeLabel(t string) string {
	switch t {
	case "tienda_barrio":
		return "Tienda de Barrio (neighbourhood corner store)"
	case "minimercado":
		return "Minimercado (small supermarket / mini-mart)"
	case "restaurante":
		return "Restaurante (sit-down restaurant)"
	case "comidas_rapidas":
		return "Comidas Rápidas (fast-food / take-away)"
	case "bar":
		return "Bar / Discoteca (bar or nightclub)"
	case "deposito_construccion":
		return "Depósito de Construcción / Ferretería (hardware store)"
	case "manufactura":
		return "Manufactura (small-scale manufacturing)"
	case "reparacion_muebles":
		return "Reparación / Servicios (repair shop or service trade)"
	case "emprendimiento_general":
		return "Emprendimiento (general entrepreneurship)"
	default:
		return t
	}
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

	s.recordTokenUsage(ctx, models.AIFeatureEnhancePhoto, s.imageModel, &geminiResp)

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

	s.recordTokenUsage(ctx, models.AIFeaturePromoBanner, s.imageModel, &geminiResp)

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

	s.recordTokenUsage(ctx, models.AIFeatureProductImage, s.imageModel, &geminiResp)

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

	s.recordTokenUsage(ctx, models.AIFeatureOCRInvoice, s.model, &geminiResp)

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

func (s *GeminiService) callImageGeneration(ctx context.Context, feature, prompt string, count int) ([]LogoResult, error) {
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
	if geminiResp.Error == nil {
		s.recordTokenUsage(ctx, feature, s.imageModel, &geminiResp)
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
	// UsageMetadata is set by the Generative Language API on success.
	// See: https://ai.google.dev/api/generate-content#UsageMetadata
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
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
