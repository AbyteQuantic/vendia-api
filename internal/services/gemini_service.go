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
	// Subject is the most important line — we lead with it. When the
	// merchant typed details ("Llaveros y utensilios de moda"), that
	// description IS the brief; the rubro fallback only kicks in when
	// the textbox was empty. Image-generation models follow concrete
	// "draw a X" instructions much better than abstract design rules,
	// so we pre-resolve the subject here instead of letting the model
	// guess between competing constraints.
	details = strings.TrimSpace(details)
	if len(details) > 240 {
		details = details[:240]
	}
	subject := resolveLogoSubject(businessType, details)

	contextNote := ""
	if details != "" {
		contextNote = fmt.Sprintf(
			"\n\nThe owner described the business in their own words: \"%s\". Use this description as the PRIMARY guide — the icon must clearly evoke what they actually sell. If they mention a colour preference, use it as the dominant accent.",
			details)
	}

	// Single-paragraph "describe-the-picture" brief. Image generation
	// models (Imagen / gemini-2.5-flash-image) follow this format far
	// better than multi-rule UI/UX checklists. The subject statement
	// goes first so the model has its anchor before any style rules.
	prompt := fmt.Sprintf(`A flat vector logo icon. Subject: %s. Render it as a single, centered pictogram filling about 60%%%% of the canvas, with generous padding all around so a circular crop never clips the subject.

Style: modern, minimalist, geometric flat-vector design. Bold solid colours, 2 to 3 colours total, high contrast. Examples of palettes that work: deep indigo + warm cream, terracotta + ivory, sage green + bone, charcoal + mustard, navy + amber. The background must be ONE solid colour — pure white or a single saturated tone — never transparent, never a gradient, never patterned.

The whole logo must be instantly readable at 24 pixels (mobile app icon size) and still look great at 512 pixels (storefront banner). Use consistent stroke weights. No photorealism. No 3D. No drop shadows. No textures.

ABSOLUTE PROHIBITION: do NOT render any letters, numbers, words, or text characters anywhere in the image — not even decorative ones, not even the brand name. The logo is pure symbol. Any text-like marks ruin the design and must be replaced with shapes.

The brand is "%s", a small business in Colombia (%s).%s

Output ONLY the finished logo image as a 1024x1024 square. No mockups, no signatures, no border frames, no watermarks.`,
		subject, businessName, businessTypeLabel(businessType), contextNote)

	results, err := s.callImageGeneration(ctx, models.AIFeatureLogoGen, prompt, 1)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// resolveLogoSubject picks the concrete pictogram the model should
// draw. When the merchant typed details ("Llaveros y utensilios de
// moda"), we extract a directive subject from those words so the
// model has a single, unambiguous anchor — image-gen models drift
// hard when fed competing options. When details are empty we fall
// back to the rubro default.
//
// The mapping is keyword-based, biased to common Spanish products in
// Colombian neighbourhood businesses. Order matters — the first match
// wins. Multi-product hints chain with " y " so the model can blend
// (e.g. "tienda + helados" → "a corner store storefront together with
// a stylised ice cream cone").
func resolveLogoSubject(businessType, details string) string {
	rubro := industryIconHint(businessType)
	if details == "" {
		return rubro
	}
	d := strings.ToLower(details)
	hits := []string{}

	add := func(symbol string) {
		for _, h := range hits {
			if h == symbol {
				return
			}
		}
		hits = append(hits, symbol)
	}

	type kw struct {
		keywords []string
		symbol   string
	}
	mapping := []kw{
		{[]string{"helado", "ice cream", "nieve"}, "a stylised ice cream cone with a generous scoop"},
		{[]string{"panaderia", "panadería", "pan ", "pasteleria", "pastelería", "reposteria", "repostería", "torta", "bakery"}, "a stylised loaf of bread or a wheat sheaf"},
		{[]string{"cafe", "café", "tinto", "barista"}, "a stylised coffee cup with a steam wisp"},
		{[]string{"pollo", "chicken", "broaster"}, "a stylised drumstick or a hen silhouette"},
		{[]string{"hamburguesa", "burger"}, "a stylised hamburger silhouette"},
		{[]string{"pizza"}, "a stylised pizza slice"},
		{[]string{"jugo", "fruta", "smoothie", "juice"}, "a stylised glass with a fruit slice on the rim"},
		{[]string{"licor", "cerveza", "bar", "ron", "aguardiente", "trago"}, "a stylised cocktail glass or beer bottle silhouette"},
		{[]string{"flor", "florist", "ramo"}, "a stylised single flower or a small bouquet silhouette"},
		{[]string{"mascota", "pet", "veterinaria"}, "a stylised dog or cat silhouette"},
		{[]string{"libro", "papeleria", "papelería", "stationery"}, "a stylised open book and pencil pair"},
		{[]string{"ropa", "moda", "boutique", "fashion"}, "a stylised hanger with a fashionable garment silhouette"},
		{[]string{"llavero", "llave", "key"}, "a stylised key-ring with two crossed keys"},
		{[]string{"accesori", "joyer", "joya"}, "a stylised gemstone or pendant silhouette"},
		{[]string{"zapato", "calzado", "shoe", "tenis"}, "a stylised sneaker silhouette"},
		{[]string{"juguete", "toy"}, "a stylised teddy bear or building-block silhouette"},
		{[]string{"belleza", "salon", "salón", "peluquer", "barber"}, "a stylised pair of scissors and a comb"},
		{[]string{"barbería", "barberia"}, "a stylised barber pole and razor"},
		{[]string{"taller", "mecanic", "auto"}, "a stylised wrench and gear pair"},
		{[]string{"tecnolog", "celular", "phone", "computad"}, "a stylised phone-and-charger or a circuit-leaf hybrid"},
		{[]string{"farmaci", "drogu", "salud", "medic"}, "a stylised mortar-and-pestle or a green cross"},
		{[]string{"verdura", "fruta", "vegetal", "organic"}, "a stylised basket of fresh produce"},
		{[]string{"carne", "carnicer", "butcher"}, "a stylised cleaver and steak silhouette"},
		{[]string{"queso", "lacte"}, "a stylised wedge of cheese with a milk drop"},
		{[]string{"miel", "honey", "abeja"}, "a stylised honey jar with a honeycomb hexagon"},
		{[]string{"costura", "modist", "sastre", "tailor", "ropa hecha"}, "a stylised needle threaded through cloth"},
		{[]string{"madera", "carpinter", "muebles"}, "a stylised hammer crossed with a wood plank"},
		{[]string{"jardin", "jardín", "planta", "vivero"}, "a stylised potted plant with a leaf accent"},
		{[]string{"limpieza", "aseo", "cleaning"}, "a stylised spray bottle and a sparkle"},
		{[]string{"helado artesanal"}, "a stylised artisanal ice cream cone with fruit accents"},
		{[]string{"frutas naturales", "frutos"}, "a stylised cluster of three fruits"},
	}
	for _, m := range mapping {
		for _, k := range m.keywords {
			if strings.Contains(d, k) {
				add(m.symbol)
				break
			}
		}
	}

	if len(hits) == 0 {
		// No keyword matched — model still gets the raw owner text
		// inside contextNote, plus the rubro fallback as a fail-safe.
		return rubro
	}
	if len(hits) == 1 {
		return hits[0]
	}
	// Combine up to 2 distinct symbols so the icon stays readable.
	if len(hits) > 2 {
		hits = hits[:2]
	}
	return hits[0] + " together with " + hits[1]
}

// industryIconHint returns a SINGLE concrete subject for image gen.
// Used as the fallback when the merchant didn't type any details —
// resolveLogoSubject() takes precedence whenever the brand-tone box
// has content. Image-generation models render one specific subject
// far better than a menu of alternatives.
func industryIconHint(businessType string) string {
	switch businessType {
	case "tienda_barrio":
		return "a stylised neighbourhood corner storefront with a striped awning and a small basket of groceries in front"
	case "minimercado":
		return "a stylised shopping cart filled with fresh produce silhouettes"
	case "restaurante":
		return "a stylised covered plate with steam wisps over a crossed fork and knife"
	case "comidas_rapidas":
		return "a stylised hamburger silhouette with a small lightning bolt accent"
	case "bar":
		return "a stylised cocktail glass with an olive on a pick"
	case "deposito_construccion":
		return "a stylised hard hat resting on a wrench"
	case "manufactura":
		return "a stylised gear with three bold teeth"
	case "reparacion_muebles":
		return "a stylised wrench and screwdriver crossed in an X"
	case "emprendimiento_general":
		return "a stylised rocket silhouette taking off"
	default:
		return "a clean abstract geometric mark of two interlocking shapes evoking a professional small business"
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
