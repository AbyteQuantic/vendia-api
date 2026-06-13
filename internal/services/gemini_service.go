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

// defaultTextModel and defaultImageModel are the IDs used when the
// caller did not configure GEMINI_MODEL / GEMINI_IMAGE_MODEL and the
// runtime discovery did not return a usable candidate. We default the
// image model to Nano Banana Pro because identity preservation on
// product photos (the "Mejorar con IA" flow) is the dominant use case
// and Pro respects the source silhouette better than the Flash tiers.
const (
	defaultTextModel  = "gemini-2.0-flash"
	defaultImageModel = "gemini-3-pro-image-preview"
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

// requestContext derives the context used for a single outbound HTTP
// call to Gemini.
//
// Spec: specs/015-ia-foto-timeouts/spec.md — FR-03 / D2.
//
// The previous code did context.WithTimeout(ctx, s.timeout) on every
// call. Because context.WithTimeout always picks the EARLIER of the
// parent deadline and now+s.timeout, an image handler that granted
// ~110s of context still saw the Gemini call cut off at s.timeout
// (30s) — shorter than a single Gemini image operation (~27s) plus
// download and upload. The handler ctx was effectively ignored.
//
// The fix: when the caller already carries a deadline, defer to it —
// the handler is the authority on how long the whole operation may
// run. s.timeout is only a safety net for callers that passed a
// context with no deadline at all. cancellation always propagates.
func (s *GeminiService) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		// Caller set a deadline — honor it exactly, never shrink it.
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.timeout)
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
	in, out := gr.UsageMetadata.InputOutput()
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
		return defaultTextModel, defaultImageModel
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("[GEMINI] ListModels HTTP %d: %.200s — using fallbacks", resp.StatusCode, string(body))
		return defaultTextModel, defaultImageModel
	}

	var listResp struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		log.Printf("[GEMINI] Failed to parse models list: %v", err)
		return defaultTextModel, defaultImageModel
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

		// Image generation model: prefer Pro tier (Nano Banana Pro)
		// over Flash for product-photo identity preservation, then
		// fall back to alphabetical ordering within the same tier.
		if strings.Contains(name, "image") || strings.Contains(name, "flash-exp") {
			if imageModel == "" {
				imageModel = name
			} else {
				candidateIsPro := strings.Contains(name, "pro")
				currentIsPro := strings.Contains(imageModel, "pro")
				switch {
				case candidateIsPro && !currentIsPro:
					imageModel = name
				case candidateIsPro == currentIsPro &&
					strings.Compare(name, imageModel) > 0:
					imageModel = name
				}
			}
		}
	}

	if textModel == "" {
		textModel = defaultTextModel
	}
	if imageModel == "" {
		imageModel = defaultImageModel
	}

	log.Printf("[GEMINI] Discovered %d models. Selected text=%s, image=%s", len(listResp.Models), textModel, imageModel)
	return textModel, imageModel
}

type InvoiceProduct struct {
	Name         string  `json:"name"`
	Presentation string  `json:"presentation,omitempty"`
	Content      string  `json:"content,omitempty"`
	Quantity     int     `json:"quantity"`
	UnitPrice    float64 `json:"unit_price"`
	TotalPrice   float64 `json:"total_price"`
	Barcode      string  `json:"barcode,omitempty"`
	ExpiryDate   string  `json:"expiry_date,omitempty"`
	Confidence   float64 `json:"confidence"`
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
3. Si una fila está borrosa, cortada o no se puede leer con certeza, IGNÓRALA completamente.
4. Los precios deben ser los números EXACTOS impresos. No calcules ni redondees.
5. Si ves un nombre de proveedor en el encabezado, extráelo. Si no, pon "Desconocido".
6. El campo "confidence" debe reflejar tu certeza real (0.0 a 1.0). Si dudas, pon < 0.7.

REGLA DE CAMPOS DEL PRODUCTO — DEBES SEPARAR INTELIGENTEMENTE:
- "name": SOLO el nombre comercial limpio del producto (ej: "Speed Max", "Coca Cola", "Arroz Diana"). SIN medidas, SIN presentación, SIN cantidades de empaque.
- "presentation": tipo de empaque/envase extraído del texto (ej: "PET", "botella", "lata", "bolsa", "caja", "paca", "sobre"). Si dice "PET X 12", la presentación es "PET". Si no se indica, dejar "".
- "content": medida/volumen/peso del producto (ej: "250ml", "500g", "1.5L", "1kg"). Extraerlo del nombre si aparece ahí. Si no se indica, dejar "".
- "quantity": cantidad de UNIDADES compradas. Si dice "X 12" o "PACA X12", la cantidad es 12. Si dice "2 UND", la cantidad es 2. Si solo hay una línea sin multiplicador, es 1.
- "barcode": si la factura muestra un código de barras, EAN o SKU numérico junto al ítem, extráelo. Si no, dejar "".

EJEMPLO de separación correcta:
Texto en factura: "SPEED MAX 250 ML PET X 12"  →  name: "Speed Max", presentation: "PET", content: "250ml", quantity: 12
Texto en factura: "ARROZ DIANA BOLSA 500G"     →  name: "Arroz Diana", presentation: "bolsa", content: "500g", quantity: 1
Texto en factura: "COCA COLA 1.5L PET X6"      →  name: "Coca Cola", presentation: "PET", content: "1.5L", quantity: 6

REGLA DE FECHA DE VENCIMIENTO ("expiry_date"):
- Busca etiquetas como "VENCE", "VENCIMIENTO", "CADUCIDAD", "EXP", "F.V.", "VTO" cerca de cada ítem.
- Normaliza a "YYYY-MM-DD". Para mes/año (ej. "12/26"), usa último día del mes ("2026-12-31").
- Si NO hay fecha visible, deja "". NO inventes fechas.
- Si hay una fecha global para toda la factura, aplícala a TODAS las líneas.

Retorna JSON estricto sin markdown:
{"provider":"nombre del proveedor","products":[{"name":"nombre limpio","presentation":"tipo envase","content":"medida","quantity":0,"unit_price":0,"total_price":0,"barcode":"","expiry_date":"","confidence":0.95}],"invoice_total":0}

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
// Colombian business. The prompt steers toward premium 3D-rendered
// icons with realistic materials (metal, enamel, glass, wood) that
// look professional at both 24px app-icon and 512px storefront sizes.
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

	// Brand identity — initial or short name. Image-gen models render
	// a SINGLE bold capital letter much more reliably than a word, so
	// we lift the first letter (or the full name when ≤ 5 chars) and
	// instruct the model to integrate it as a subtle, ornamental
	// element of the silhouette — NOT as a banner. This grounds the
	// pictogram in the merchant's actual brand instead of producing a
	// pure symbol that could belong to any tienda. The "subtle"
	// framing is on purpose: heavy text typically renders garbled.
	identityHint := ""
	trimmed := strings.TrimSpace(businessName)
	if trimmed != "" {
		// Use the full name if very short, else just the initial.
		marker := ""
		runes := []rune(trimmed)
		if len(runes) <= 5 {
			marker = strings.ToUpper(trimmed)
		} else {
			marker = strings.ToUpper(string(runes[0]))
		}
		identityHint = fmt.Sprintf(`

BRAND IDENTITY MARKER: integrate the bold capital letter%s "%s" as a SUBTLE structural element of the icon (e.g. shaped into the negative space, woven into the silhouette, or carved into one of the symbol's strokes). The letter must NOT appear as a banner or floating text — it should feel inseparable from the symbol, almost hidden in the shape, so the logo carries identity without becoming a typographic monogram.`,
			ifPlural(len(runes) <= 5), marker)
	}

	// Professional 3D logo brief. Gemini image models (Nano Banana 2)
	// respond well to concrete scene descriptions with material/lighting
	// cues. Subject FIRST, style rules SECOND, identity marker LAST.
	prompt := fmt.Sprintf(`A premium 3D-rendered logo icon for a real business. Subject: %s. Render it as a single, centered 3D object or sculptural emblem filling about 65%%%% of the canvas, with generous padding all around so a circular crop never clips the subject.

STYLE (critical — follow precisely):
- Hyper-polished 3D render with studio lighting: one soft key light from the upper left, a subtle rim light on the right edge, and a gentle ambient fill.
- Materials must feel REAL and tactile: brushed metal, glossy enamel, matte ceramic, polished wood, frosted glass, or embossed leather — pick the 1-2 materials that best match the business type.
- Subtle depth: soft drop shadows beneath the icon, gentle ambient occlusion where surfaces meet, slight bevel on edges. The icon should feel like a physical badge you could pick up.
- Rich but controlled palette: 2-3 colours maximum. Use saturated, professional tones — deep emerald, royal cobalt, warm amber, burgundy, charcoal, ivory, copper, or gold accents. Colour choices should DIRECTLY reflect the business identity (e.g. greens for organic/natural, warm reds for food, blues for trust/services).
- Background: ONE solid colour — either pure white (#FFFFFF) or a deep matte tone that contrasts with the icon. Never transparent, never a gradient, never patterned.
- The overall feel should be: Apple App Store featured icon quality — clean, modern, premium, instantly recognizable.

CRITICAL RULES:
- The icon must be DIRECTLY related to what the business actually sells or does. A bakery gets bread/wheat, a key shop gets keys, a bar gets cocktails — NEVER generic abstract shapes.
- Must be instantly readable at 24px (mobile app icon) and stunning at 512px (storefront banner).
- Do NOT render any text, words, taglines, or watermarks. The ONLY exception is the brand-identity marker below — and even that is optional.
- No flat vector art. No clipart. No cartoon style. No watercolour. Think premium product rendering.

The brand is "%s", a small business in Colombia (%s).%s%s

Output ONLY the finished logo as a 1024x1024 square image. No mockups, no frames, no signatures.`,
		subject, businessName, businessTypeLabel(businessType), contextNote, identityHint)

	results, err := s.callImageGeneration(ctx, models.AIFeatureLogoGen, prompt, 1)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// ifPlural appends "s" to the word "letter" when we're embedding the
// full short business name (more than one character). A tiny helper
// that keeps the prompt grammatically correct without branching the
// whole sentence.
func ifPlural(plural bool) string {
	if plural {
		return "s"
	}
	return ""
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
		{[]string{"helado", "ice cream", "nieve"}, "a 3D ice cream cone with a glossy swirl scoop, waffle texture on the cone, and a drip of melted cream"},
		{[]string{"panaderia", "panadería", "pan ", "pasteleria", "pastelería", "reposteria", "repostería", "torta", "bakery"}, "a 3D golden-crusted artisan bread loaf with a wheat sheaf, warm ceramic texture"},
		{[]string{"cafe", "café", "tinto", "barista"}, "a 3D glossy ceramic coffee cup with realistic steam wisps and a coffee bean accent"},
		{[]string{"pollo", "chicken", "broaster"}, "a 3D crispy golden fried chicken drumstick with realistic breading texture"},
		{[]string{"hamburguesa", "burger"}, "a 3D gourmet hamburger with glossy bun, melted cheese, and fresh lettuce layers"},
		{[]string{"pizza"}, "a 3D pizza slice with stretchy melted cheese, pepperoni, and a golden crust"},
		{[]string{"jugo", "fruta", "smoothie", "juice"}, "a 3D frosted glass filled with vibrant juice and a fresh fruit slice on the rim"},
		{[]string{"licor", "cerveza", "bar", "ron", "aguardiente", "trago"}, "a 3D elegant cocktail glass with ice cubes and a citrus garnish, polished glass material"},
		{[]string{"flor", "florist", "ramo"}, "a 3D bouquet of roses and wildflowers with dewy petals and a wrapped stem"},
		{[]string{"mascota", "pet", "veterinaria"}, "a 3D friendly dog and cat sitting together, soft fur texture, warm lighting"},
		{[]string{"libro", "papeleria", "papelería", "stationery"}, "a 3D open hardcover book with a glossy pencil, embossed leather texture on the cover"},
		{[]string{"ropa", "moda", "boutique", "fashion"}, "a 3D elegant dress on a polished chrome hanger, silk fabric draping naturally"},
		{[]string{"llavero", "llave", "key"}, "a 3D ornate brass key-ring with two polished vintage keys crossed, metallic sheen and engraved details"},
		{[]string{"accesori", "joyer", "joya"}, "a 3D sparkling gemstone pendant on a polished gold chain, faceted crystal reflections"},
		{[]string{"zapato", "calzado", "shoe", "tenis"}, "a 3D premium sneaker with detailed stitching, rubber sole texture, and fabric mesh"},
		{[]string{"juguete", "toy"}, "a 3D plush teddy bear with soft velvet texture and colourful building blocks beside it"},
		{[]string{"belleza", "salon", "salón", "peluquer", "barber"}, "a 3D rose-gold scissors and a polished comb crossed elegantly, metallic sheen"},
		{[]string{"barbería", "barberia"}, "a 3D classic barber pole with red-white-blue spirals and a chrome straight razor"},
		{[]string{"taller", "mecanic", "auto"}, "a 3D chrome wrench and brushed-steel gear interlocked, industrial metallic finish"},
		{[]string{"tecnolog", "celular", "phone", "computad"}, "a 3D sleek smartphone with a glowing screen and a circuit-board pattern accent"},
		{[]string{"farmaci", "drogu", "salud", "medic"}, "a 3D marble mortar and pestle with a glowing green cross, clean medical aesthetic"},
		{[]string{"verdura", "fruta", "vegetal", "organic"}, "a 3D woven basket overflowing with vibrant fresh produce — tomatoes, bananas, lettuce"},
		{[]string{"carne", "carnicer", "butcher"}, "a 3D polished steel cleaver beside a premium marbled steak on a wooden cutting board"},
		{[]string{"queso", "lacte"}, "a 3D golden wedge of aged cheese with realistic holes and a glossy milk drop"},
		{[]string{"miel", "honey", "abeja"}, "a 3D glass honey jar with golden dripping honey and a honeycomb hexagon accent"},
		{[]string{"costura", "modist", "sastre", "tailor", "ropa hecha"}, "a 3D silver sewing needle with thread looping through luxurious fabric"},
		{[]string{"madera", "carpinter", "muebles"}, "a 3D polished hammer crossed with a wood plank showing natural grain texture"},
		{[]string{"jardin", "jardín", "planta", "vivero"}, "a 3D terracotta pot with a lush green plant, detailed leaf veins and soil texture"},
		{[]string{"limpieza", "aseo", "cleaning"}, "a 3D spray bottle with a sparkle burst effect and chrome nozzle"},
		{[]string{"helado artesanal"}, "a 3D artisanal ice cream cone with fruit toppings, waffle texture, and a glossy glaze"},
		{[]string{"frutas naturales", "frutos"}, "a 3D cluster of three tropical fruits with dewy skin and vibrant colours"},
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
		return "a 3D miniature corner storefront with a striped awning, warm wooden shelves, and a small basket of groceries — brick and wood textures"
	case "minimercado":
		return "a 3D polished shopping cart filled with vibrant fresh produce, chrome metal cart with glossy fruits"
	case "restaurante":
		return "a 3D silver cloche lid lifting with steam wisps, crossed fork and knife in brushed steel beneath"
	case "comidas_rapidas":
		return "a 3D gourmet burger with glossy bun and melted cheese, a small neon lightning bolt accent"
	case "bar":
		return "a 3D crystal cocktail glass with ice cubes, a citrus garnish, and polished glass reflections"
	case "deposito_construccion":
		return "a 3D yellow hard hat resting on a chrome wrench, industrial textures with concrete and steel"
	case "manufactura":
		return "a 3D brushed-steel gear mechanism with interlocking teeth, industrial metallic finish"
	case "reparacion_muebles":
		return "a 3D chrome wrench and screwdriver crossed, with a polished wooden furniture accent"
	case "emprendimiento_general":
		return "a 3D sleek rocket with metallic body and flame exhaust, launching upward with energy trails"
	default:
		return "a 3D polished emblem of two interlocking geometric shapes in brushed metal and enamel, evoking a professional brand"
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

// buildEnhancePhotoPrompt assembles the faithful product-photography
// EDIT instruction passed to Gemini's image model.
//
// Spec: specs/017-ia-mejora-fiel-producto/spec.md — FR-01..FR-04.
//
// The bug it fixes: the previous prompt induced the model to
// REGENERATE the product from its name/description (productInfo)
// instead of EDITING the supplied photo. A merchant photographed a
// specific Kuromi-character keychain; the AI returned generic metal
// keychains in a bag — it rebuilt the product from the word
// "Llaveros" and its training prior, discarding the real object.
//
// The rewrite reframes the task as a strict EDIT, not a generation:
// the attached image IS the product and the ONLY source of truth for
// shape, colour, proportions, details, text and brand. The model may
// ONLY cut the product out of its background and place it on pure
// white with studio lighting — it is explicitly forbidden to replace,
// redesign, reinvent, beautify or substitute the product itself.
// productInfo is now a context hint ("the product is a {productInfo}")
// — never a generation target. Extracted so unit tests can pin this
// contract and stop a future refactor reintroducing the regression.
func buildEnhancePhotoPrompt(productInfo string) string {
	contextHint := ""
	if productInfo != "" {
		contextHint = fmt.Sprintf(
			"\n\nFor context only, the product is a %s. Use this purely as a hint to understand what the object is — it is NOT a description to generate from. The attached photo always overrides this hint.",
			productInfo)
	}
	return fmt.Sprintf(`You are a professional PRODUCT PHOTO RETOUCHER. Your job is to EDIT the attached photograph, NOT to create, generate, illustrate or imagine a new product.

THE ATTACHED IMAGE IS THE PRODUCT. It is the one and only source of truth for the object's shape, silhouette, proportions, colours, materials, decorative details, accessories, printed text and brand. You are retouching THIS exact object — you are not designing a product.%s

YOUR ONLY ALLOWED EDITS:
- Cut the product out of its current background (table, hands, clutter, environment shadows) and place it on a pure white background (#FFFFFF), clean and seamless.
- Light it with soft, even studio lighting, as in a professional e-commerce product shoot.
- Add one subtle, soft contact shadow beneath the product so it sits naturally on the white surface.
- Center the product in the frame with comfortable margin around it, square 1:1 framing, catalog composition.
- Gently clean the product's surface (dust, smudges, fingerprints) WITHOUT changing its shape, and refine exposure/saturation WITHOUT shifting any hue.

STRICTLY FORBIDDEN (these cause the exact bug we are fixing):
- DO NOT replace the product with a different object, a "cleaner" version, or a stock/official version of what you think it is.
- DO NOT redesign, restyle, reinterpret or "beautify" the product itself.
- DO NOT reinvent or redraw the product, its face, characters, figures, logos, packaging or labels.
- DO NOT generate a different product based on the product name or your prior knowledge of any brand or character.
- DO NOT add or remove any element, accessory, ring, hook, cord, tag, button, eye or detail. Keep every element exactly where the photo shows it, in the same count, position and shape.
- DO NOT change any colour's hue, and DO NOT alter any printed text or brand mark.

If you recognise the product (a known character, a famous brand, a typical package), IGNORE that knowledge — the photo is the only valid reference. When in doubt, keep exactly what the photo shows; never invent.

The product in your result MUST be recognisably the same product as in the original photo: a person comparing both images must say "it is the same product, just photographed better." Same object, same identity — only the background, lighting and framing improve.

Output: a clean, professional e-commerce catalog photo of the SAME product on a pure white background, centered, with soft studio lighting and a subtle shadow. No extra text, no added logos, no watermarks.`, contextHint)
}

// EnhancePhoto generates a professional e-commerce product photo.
// productInfo is optional context (e.g., "Coca-Cola Botella 350ml").
func (s *GeminiService) EnhancePhoto(ctx context.Context, imageData []byte, mimeType string, productInfo string) ([]byte, error) {
	// Low temperature: a faithful retouch, never a transformation.
	return s.enhanceImagesWithPrompt(ctx,
		[]ReferenceImage{{MimeType: mimeType, Data: imageData}},
		buildEnhancePhotoPrompt(productInfo), models.AIFeatureEnhancePhoto, 0.2)
}

// CleanSignature isolates the handwritten signature from a photo: removes the
// paper/background/shadows and keeps only the crisp dark strokes on a clean
// white background, ready to composite on a certificate. Low temperature — a
// faithful extraction, never a redraw.
func (s *GeminiService) CleanSignature(ctx context.Context, imageData []byte, mimeType string) ([]byte, error) {
	const prompt = `Extrae ÚNICAMENTE la firma manuscrita de esta foto. Elimina por completo el fondo (papel, líneas, sombras, textura) y deja SOLO los trazos oscuros de la firma, nítidos y limpios, sobre un fondo BLANCO uniforme. No agregues texto, marcos ni adornos; no inventes trazos. La firma debe quedar recortada y centrada, lista para colocar en un certificado.`
	return s.enhanceImagesWithPrompt(ctx,
		[]ReferenceImage{{MimeType: mimeType, Data: imageData}},
		prompt, models.AIFeatureEnhancePhoto, 0.2)
}

// enhanceImagesWithPrompt edits attached image(s) with a free-form instruction
// (image-to-image). Shared by the product retoucher and the event-asset
// improver. Multiple images let the caller pass a base piece + a face/scene
// reference. temperature controls how much it may transform the source.
func (s *GeminiService) enhanceImagesWithPrompt(ctx context.Context, images []ReferenceImage, prompt, feature string, temperature float64) ([]byte, error) {
	parts := make([]map[string]any, 0, len(images)+1)
	for _, img := range images {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MimeType
		if mime == "" {
			mime = "image/png"
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
			"temperature":        temperature,
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.imageModel, s.apiKey)

	reqCtx, cancel := s.requestContext(ctx)
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

	s.recordTokenUsage(ctx, feature, s.imageModel, &geminiResp)

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

	reqCtx, cancel := s.requestContext(ctx)
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

// buildGenerateProductPrompt assembles the text-to-image prompt for
// "Generar foto con IA" — generating a catalog photo for a product
// that has NO source photo, from its name alone.
//
// Spec: specs/021-ia-generacion-respeta-tipo/spec.md — FR-01..FR-04.
//
// The bug it fixes: a "Llavero Hello Kitty" with presentation "Bolsa"
// generated a Hello Kitty PURSE instead of a keychain. Two causes:
//  1. The old prompt fed a flat string ("Llavero Hello Kitty Bolsa")
//     and let the famous character ("Hello Kitty") — a strong model
//     prior — outweigh the product type ("Llavero"), so it drew
//     generic character merch (a purse/wallet/plush).
//  2. The product's `presentation` ("Bolsa" = packaging) was glued
//     into the object text, so the model read "the object is a bag".
//
// The rewrite separates the inputs and gives explicit, forceful
// instructions: the product TYPE (main noun of the name) is the
// physical object to draw; the brand/character is ONLY the theme /
// printed decoration; the presentation is packaging context that must
// NEVER be drawn as the object. `name` and `presentation` arrive as
// distinct, labelled fields — never concatenated — so the model can
// never misread "Llavero ... Bolsa" as one object phrase.
//
// Extracted as a builder so unit tests can pin this contract and stop
// a future refactor from reintroducing the regression. EnhancePhoto
// (F017) is a separate, faithful EDIT path and is intentionally NOT
// touched here.
func buildGenerateProductPrompt(name, presentation string) string {
	name = strings.TrimSpace(name)
	presentation = strings.TrimSpace(presentation)

	packagingLine := ""
	if presentation != "" {
		packagingLine = fmt.Sprintf(`

PACKAGING CONTEXT (read carefully — this is a trap):
- This product is sold in the following packaging/presentation: "%s".
- The packaging/presentation is ONLY commercial context — it is NOT the object to draw.
- It is STRICTLY FORBIDDEN to draw the packaging as the product. If the packaging word is "Bolsa" (bag), "Lata" (can), "Caja" (box), "Paquete" (pack), "Frasco" (jar) or "Unidad" (unit), you must STILL draw the product type from the name above — never a bag, a can, a box or a jar.
- Example of the exact mistake to avoid: name "Llavero Hello Kitty" + packaging "Bolsa" → you draw a KEYCHAIN with a Hello Kitty theme. You do NOT draw a bag, a purse or a pouch.`, presentation)
	}

	return fmt.Sprintf(`You are a professional e-commerce product photographer. Generate ONE realistic catalog photo of the product described below.

THE PRODUCT TO DRAW: "%s"

WHAT TO DRAW — READ THIS FIRST (most important rule):
- The physical object you must draw is the TYPE of product — the main noun of the product name above (for example: "llavero" = a keychain, "gaseosa" = a soda bottle, "camiseta" = a t-shirt, "cuaderno" = a notebook). That main noun, and ONLY that noun, decides WHICH object appears in the image.
- If the name also contains a brand or character ("Hello Kitty", "Kuromi", "Coca-Cola", "Spider-Man"), that brand or character is ONLY the theme — the print, pattern, colour scheme or decoration ON the object. It changes how the object LOOKS, it never changes WHICH object it is.
- It is STRICTLY FORBIDDEN to generate generic character merchandise instead of the requested type. Do NOT draw a purse, a wallet, a coin pouch, a backpack, a plush toy or a figurine just because the name mentions a famous character. If the type says "llavero", you draw a keychain decorated with that character — nothing else.
- If the product type is genuinely ambiguous or missing, draw a simple, neutral generic product — but NEVER a piece of packaging.%s

PHOTOGRAPHY STYLE:
- The product is centered, complete and fully visible, never cropped, never touching the edges.
- Pure white background (#FFFFFF), clean, no background shadows; one subtle soft contact shadow under the product.
- Soft, even studio lighting. Colours realistic, sharp and vibrant. Square 1:1 framing.
- No added text, no extra logos, no watermarks.
- Amazon / MercadoLibre style: the product isolated on white, e-commerce catalog quality. It must look like a real photo of the real product.

Output ONLY the finished product photo. No mockups, no frames, no captions.`, name, packagingLine)
}

// GenerateProductImage creates a product image from a product name and
// optional packaging/presentation (no source photo needed).
//
// Spec: specs/021-ia-generacion-respeta-tipo/spec.md — FR-01..FR-04.
//
// `name` and `presentation` are intentionally SEPARATE parameters:
// the caller must NOT pre-concatenate them. The product TYPE lives in
// `name` (its main noun is the object to draw); `presentation` is
// packaging context only. See buildGenerateProductPrompt for the full
// rationale on why mixing the two produced the wrong product.
func (s *GeminiService) GenerateProductImage(ctx context.Context, name, presentation string) ([]byte, error) {
	return s.generateImageFromTextPrompt(ctx,
		buildGenerateProductPrompt(name, presentation), models.AIFeatureProductImage)
}

// GenerateDishImage creates a SAMPLE catalog photo of a restaurant dish from
// its name, description and optional presentation (how it is served). Unlike
// GenerateProductImage (whose prompt has retail "don't draw the packaging"
// traps), this uses a food-photography prompt: the description drives the
// visible ingredients so the sample is much more accurate (F043 — el fundador
// pidió que la imagen se base en título + descripción + presentación).
func (s *GeminiService) GenerateDishImage(ctx context.Context, name, description, presentation string) ([]byte, error) {
	return s.generateImageFromTextPrompt(ctx,
		buildGenerateDishPrompt(name, description, presentation), models.AIFeatureProductImage)
}

// generateImageFromTextPrompt runs a text-only image generation and returns the
// first image's bytes. Shared by GenerateProductImage and GenerateDishImage so
// the proven HTTP/decoding path lives in one place.
func (s *GeminiService) generateImageFromTextPrompt(ctx context.Context, prompt, feature string) ([]byte, error) {
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

	reqCtx, cancel := s.requestContext(ctx)
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

	s.recordTokenUsage(ctx, feature, s.imageModel, &geminiResp)

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

// buildGenerateDishPrompt assembles a food-photography prompt for a SAMPLE dish
// image. The description is the strongest signal for the visible ingredients,
// so the sample matches what the diner will actually get; presentation (how it
// is served — "en plato", "para llevar", "en vaso") refines the composition.
func buildGenerateDishPrompt(name, description, presentation string) string {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	presentation = strings.TrimSpace(presentation)

	descLine := ""
	if description != "" {
		descLine = fmt.Sprintf(`

INGREDIENTS & PREPARATION (the strongest signal — draw exactly this):
- The dish is described as: "%s".
- Make the visible ingredients and preparation match this description as closely as possible, so the photo looks like the real dish the customer will receive. Do NOT add famous garnishes or sides that are not implied by the name or this description.`, description)
	}

	servingLine := ""
	if presentation != "" {
		servingLine = fmt.Sprintf(`

HOW IT IS SERVED:
- This dish is served like this: "%s" (for example: on a plate, in a takeaway box, in a glass/cup, in a bowl). Compose the photo with the dish presented in that way.`, presentation)
	}

	return fmt.Sprintf(`You are a professional FOOD photographer for a restaurant menu. Generate ONE realistic, appetizing catalog photo of the dish described below.

THE DISH: "%s"%s%s

PHOTOGRAPHY STYLE:
- A real, appetizing photo of the prepared dish, freshly served, top-quality menu photography.
- The dish is centered, complete and fully visible, never cropped, never touching the edges.
- Clean, softly lit background (neutral white or a subtle wooden/table surface), natural soft studio lighting, realistic and vibrant colours, square 1:1 framing.
- One subtle soft shadow so the dish sits naturally on the surface.
- No added text, no logos, no watermarks, no people, no hands, no menus or price tags.

IMPORTANT — this is a SAMPLE illustration to help the merchant: keep it realistic and faithful to the name and description; never invent a different dish or a fancier version than described.

Output ONLY the finished food photo.`, name, descLine, servingLine)
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

	reqCtx, cancel := s.requestContext(ctx)
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

	reqCtx, cancel := s.requestContext(ctx)
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

// GenerateText runs a plain TEXT-only generation (no image) and returns the
// model's text. Backs AI-assisted copy like the event description agent.
func (s *GeminiService) GenerateText(ctx context.Context, prompt string) (string, error) {
	if s == nil || s.apiKey == "" {
		return "", fmt.Errorf("gemini not configured")
	}
	payload := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{"temperature": 0.7},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", s.model, s.apiKey)

	reqCtx, cancel := s.requestContext(ctx)
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
		return "", fmt.Errorf("read gemini response: %w", err)
	}
	var gr geminiResponse
	if err := json.Unmarshal(respBody, &gr); err != nil {
		return "", fmt.Errorf("parse gemini response: %w", err)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini response")
	}
	return strings.TrimSpace(gr.Candidates[0].Content.Parts[0].Text), nil
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

	reqCtx, cancel := s.requestContext(ctx)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse image gen response: %w", err)
	}

	if geminiResp.Error != nil {
		return nil, fmt.Errorf("gemini error %d: %s", geminiResp.Error.Code, geminiResp.Error.Message)
	}

	s.recordTokenUsage(ctx, feature, s.imageModel, &geminiResp)

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
	UsageMetadata GemUsageMetadata `json:"usageMetadata"`
	Candidates    []struct {
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
