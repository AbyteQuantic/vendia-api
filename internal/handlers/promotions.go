package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PromoFinancials summarises the profit math for a combo promotion.
// Exposed as a standalone type so the same logic can be invoked from
// unit tests and from any handler that needs a preview (create and
// the future "editar promo" flow).
type PromoFinancials struct {
	TotalCost       float64 `json:"total_cost"`       // sum of purchase_price * quantity
	TotalRegular    float64 `json:"total_regular"`    // sum of price * quantity at current shelf price
	TotalPromo      float64 `json:"total_promo"`      // what the customer pays for the combo
	DiscountAmount  float64 `json:"discount_amount"`  // total_regular - total_promo
	DiscountPercent float64 `json:"discount_percent"` // 0..100
	NetProfit       float64 `json:"net_profit"`       // total_promo - total_cost (negative = loss)
	IsProfitable    bool    `json:"is_profitable"`
}

// calculatePromoFinancials is the pure-function truth for combo math.
// Rounds everything to 2 decimals and clamps percentages so the Flutter
// calculator and the backend always agree. The caller guarantees that
// each PromotionItem has a matching product; missing products are
// charged at 0 cost/regular which makes the discount look artificially
// generous — surface-level validators must reject incomplete combos.
func calculatePromoFinancials(items []models.PromotionItem, productLookup map[string]models.Product) PromoFinancials {
	var f PromoFinancials
	for _, it := range items {
		p, ok := productLookup[it.ProductID]
		if !ok {
			continue
		}
		qty := float64(it.Quantity)
		f.TotalCost += p.PurchasePrice * qty
		f.TotalRegular += p.Price * qty
		f.TotalPromo += it.PromoPrice * qty
	}
	f.DiscountAmount = f.TotalRegular - f.TotalPromo
	if f.TotalRegular > 0 {
		f.DiscountPercent = (f.DiscountAmount / f.TotalRegular) * 100
		if f.DiscountPercent < 0 {
			f.DiscountPercent = 0
		}
	}
	f.NetProfit = f.TotalPromo - f.TotalCost
	f.IsProfitable = f.NetProfit >= 0

	// Two-decimal rounding to avoid binary-float noise in the UI.
	round2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	f.TotalCost = round2(f.TotalCost)
	f.TotalRegular = round2(f.TotalRegular)
	f.TotalPromo = round2(f.TotalPromo)
	f.DiscountAmount = round2(f.DiscountAmount)
	f.DiscountPercent = round2(f.DiscountPercent)
	f.NetProfit = round2(f.NetProfit)
	return f
}

func ListPromotions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var promotions []models.Promotion
		if err := db.Preload("Items").
			Where("tenant_id = ? AND is_active = true", tenantID).
			Order("created_at DESC").
			Find(&promotions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener promociones"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": promotions})
	}
}

func CreatePromotion(db *gorm.DB) gin.HandlerFunc {
	// CreatePromotion accepts EITHER legacy single-product payloads OR
	// combo payloads (items[]). The two shapes coexist: if `items` is
	// present and non-empty, we treat the request as a combo and persist
	// rows into promotion_items; otherwise we fall through to the
	// original single-product write (used by the POS "Sugerencias"
	// shortcut, which hasn't changed).
	type ItemReq struct {
		ProductID  string  `json:"product_id"  binding:"required"`
		Quantity   int     `json:"quantity"    binding:"required,gt=0"`
		PromoPrice float64 `json:"promo_price" binding:"required,gte=0"`
	}
	type Request struct {
		ID             string     `json:"id"`
		Name           string     `json:"name"`
		Description    string     `json:"description"`
		PromoType      string     `json:"promo_type"`
		BannerImageURL string     `json:"banner_image_url"`
		StartDate      *time.Time `json:"start_date"`
		EndDate        *time.Time `json:"end_date"`
		StockLimit     *int       `json:"stock_limit"`

		// Combo mode.
		Items []ItemReq `json:"items"`

		// Legacy single-product mode — only one of these should arrive.
		ProductUUID string  `json:"product_uuid"`
		PromoPrice  float64 `json:"promo_price"`
		ExpiresAt   *string `json:"expires_at"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		promoType := req.PromoType
		if promoType == "" {
			promoType = "discount"
		}

		// ── Combo branch ───────────────────────────────────────────────
		if len(req.Items) > 0 {
			if strings.TrimSpace(req.Name) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "la promoción combo requiere un nombre"})
				return
			}

			productIDs := make([]string, 0, len(req.Items))
			for _, it := range req.Items {
				productIDs = append(productIDs, it.ProductID)
			}
			var products []models.Product
			if err := db.Where("tenant_id = ? AND id IN ?", tenantID, productIDs).
				Find(&products).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al validar productos"})
				return
			}
			if len(products) != len(productIDs) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "uno o más productos no existen en este negocio"})
				return
			}

			promo := models.Promotion{
				TenantID:       tenantID,
				Name:           req.Name,
				Description:    req.Description,
				BannerImageURL: req.BannerImageURL,
				StartDate:      req.StartDate,
				EndDate:        req.EndDate,
				StockLimit:     req.StockLimit,
				PromoType:      promoType,
				IsActive:       true,
			}
			if req.ID != "" {
				promo.ID = req.ID
			}

			// We tag which step blew up so when the UI shows the
			// detail the tendero (or Ops) can tell "promotion row"
			// apart from "item row" failures without digging logs.
			var failedStep string
			txErr := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&promo).Error; err != nil {
					failedStep = "promotion"
					return err
				}
				for _, it := range req.Items {
					item := models.PromotionItem{
						BaseModel:   models.BaseModel{ID: uuid.NewString()},
						PromotionID: promo.ID,
						ProductID:   it.ProductID,
						Quantity:    it.Quantity,
						PromoPrice:  it.PromoPrice,
					}
					if err := tx.Create(&item).Error; err != nil {
						failedStep = "promotion_item"
						return err
					}
					promo.Items = append(promo.Items, item)
				}
				return nil
			})
			if txErr != nil {
				// Surface the real driver error to the client just
				// like we already do in admin_catalogs.go — masking
				// it as "error al crear promoción combo" locked the
				// tendero out of the wizard with nothing to act on.
				// The user-facing "error" string stays unchanged so
				// existing toast copy keeps working.
				log.Printf("[PROMO] create combo failed (step=%s, tenant=%s, items=%d, banner_len=%d): %v",
					failedStep, tenantID, len(req.Items), len(req.BannerImageURL), txErr)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error":  "error al crear promoción combo",
					"detail": txErr.Error(),
					"step":   failedStep,
				})
				return
			}

			c.JSON(http.StatusCreated, gin.H{"data": promo})
			return
		}

		// ── Legacy single-product branch ───────────────────────────────
		if req.ProductUUID == "" || req.PromoPrice <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "envíe items[] para combo o product_uuid + promo_price para promo simple"})
			return
		}

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", req.ProductUUID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		promo := models.Promotion{
			TenantID:    tenantID,
			ProductUUID: middleware.UUIDPtr(req.ProductUUID),
			ProductName: product.Name,
			OrigPrice:   product.Price,
			PromoPrice:  req.PromoPrice,
			PromoType:   promoType,
			Description: req.Description,
			IsActive:    true,
			ExpiresAt:   req.ExpiresAt,
		}
		if req.ID != "" {
			promo.ID = req.ID
		}

		if err := db.Create(&promo).Error; err != nil {
			log.Printf("[PROMO] create single failed (tenant=%s, product=%s): %v",
				tenantID, req.ProductUUID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al crear promoción",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": promo})
	}
}

// GenerateMarketingBanner calls the AI image-generation pipeline to
// produce a promotional banner. Kept in the promotions handler file so
// it shares the request/response style of the rest of the module.
//
// Prompt engineering prioritises retail-advertising composition and
// readable embedded copy — Gemini's image model is inconsistent with
// typography, so we give it a simple, well-structured instruction and
// let the shopkeeper accept or regenerate.
//
// 2026-04-23: prompt V2 injects full value-proposition (precio normal
// tachado, precio promo gigante, ahorro, % OFF, nombre del negocio).
// Feedback del PO: los banners V1 se veían como "fotos de menú" porque
// el prompt sólo recibía un DiscountText suelto — ahora le damos al
// modelo la estructura financiera completa con jerarquía tipográfica
// explícita y anti-patrones listados.
// ImageSourceType — indica de dónde salen los "anclajes visuales"
// que Gemini usará para renderizar los productos en el banner.
const (
	ImageSourceCatalog     = "CATALOG_PHOTOS" // usa las fotos reales del catálogo del tenant
	ImageSourceAIGenerated = "AI_GENERATED"   // Gemini los genera desde cero (default)
)

// maxCatalogRefImages limits how many product photos we fetch per
// banner request. Gemini image models get confused beyond ~4
// references — and each image adds ~200–500 KB to the payload.
const maxCatalogRefImages = 4

// catalogFetchTimeout bounds the TOTAL time we'll spend pulling
// catalogue photos across the network before starting the Gemini
// call. If a tenant's CDN is slow we'd rather fall back to text-only
// generation than time the whole request out.
const catalogFetchTimeout = 8 * time.Second

func GenerateMarketingBanner(geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	type Request struct {
		// Legacy (V1) — aún requeridos para compatibilidad del cliente.
		PromoName    string   `json:"promo_name"    binding:"required"`
		Products     []string `json:"products"      binding:"required,min=1"`
		DiscountText string   `json:"discount_text"`
		Tone         string   `json:"tone"` // "vibrante" | "elegante" | "urgente" | ""

		// V2 — value-proposition injection. Todos opcionales: si no
		// llegan caemos al prompt V1. Esto permite que un cliente Flutter
		// viejo no se rompa mientras se despliega la nueva versión.
		TenantName     string `json:"tenant_name"`
		ComboTitle     string `json:"combo_title"`
		NormalPriceStr string `json:"normal_price_str"`
		PromoPriceStr  string `json:"promo_price_str"`
		DiscountStr    string `json:"discount_str"`
		SavingsStr     string `json:"savings_str"`

		// V3 — image sourcing. Le permite al tendero escoger entre
		// "usa mis fotos reales" (CATALOG_PHOTOS) o "genera unas
		// fotos apetitosas" (AI_GENERATED). Default = AI_GENERATED.
		ImageSourceType  string   `json:"image_source_type"`
		CatalogImageURLs []string `json:"catalog_image_urls"`
	}

	return func(c *gin.Context) {
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Normalise image sourcing. If CATALOG_PHOTOS is requested but
		// no URLs arrive, degrade silently to AI_GENERATED so the
		// request still succeeds — the PO prefers any banner over a
		// 400 error the tendero has to debug.
		source := strings.ToUpper(strings.TrimSpace(req.ImageSourceType))
		if source != ImageSourceCatalog && source != ImageSourceAIGenerated {
			source = ImageSourceAIGenerated
		}
		var refImages []services.ReferenceImage
		if source == ImageSourceCatalog {
			urls := req.CatalogImageURLs
			if len(urls) > maxCatalogRefImages {
				urls = urls[:maxCatalogRefImages]
			}
			fetchCtx, cancelFetch := context.WithTimeout(
				c.Request.Context(), catalogFetchTimeout)
			refImages = fetchCatalogReferenceImages(fetchCtx, urls)
			cancelFetch()
			if len(refImages) == 0 {
				// Usuario pidió CATALOG_PHOTOS pero ninguna URL se pudo
				// descargar — caemos a AI_GENERATED y logeamos para
				// diagnóstico.
				log.Printf("[BANNER] CATALOG_PHOTOS requested but 0 of %d URLs fetched — falling back to AI_GENERATED", len(urls))
				source = ImageSourceAIGenerated
			}
		}

		prompt := BuildPromoBannerPrompt(PromoBannerPromptInput{
			PromoName:       req.PromoName,
			Products:        req.Products,
			DiscountText:    req.DiscountText,
			Tone:            req.Tone,
			TenantName:      req.TenantName,
			ComboTitle:      req.ComboTitle,
			NormalPriceStr:  req.NormalPriceStr,
			PromoPriceStr:   req.PromoPriceStr,
			DiscountStr:     req.DiscountStr,
			SavingsStr:      req.SavingsStr,
			ImageSourceType: source,
			NumRefImages:    len(refImages),
		})

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID),
			45*time.Second,
		)
		defer cancel()

		imageBytes, err := geminiSvc.GeneratePromoBanner(ctx, prompt, refImages)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo generar el banner con IA", "detail": err.Error()})
			return
		}

		// Upload to storage if available — otherwise hand the raw bytes
		// back as a data URL so offline dev environments still work.
		var url string
		if storageSvc != nil {
			objectKey := fmt.Sprintf("%s/%s.png", tenantID, uuid.NewString())
			uploaded, upErr := storageSvc.Upload(ctx, "promo-banners", objectKey, imageBytes, "image/png")
			if upErr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo guardar el banner", "detail": upErr.Error()})
				return
			}
			url = uploaded
		} else {
			url = "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"banner_url":  url,
				"prompt_used": prompt,
			},
		})
	}
}

func UpdatePromotion(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PromoPrice  *float64 `json:"promo_price"`
		Description *string  `json:"description"`
		IsActive    *bool    `json:"is_active"`
		ExpiresAt   *string  `json:"expires_at"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var promo models.Promotion
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&promo).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.PromoPrice != nil {
			updates["promo_price"] = *req.PromoPrice
		}
		if req.Description != nil {
			updates["description"] = *req.Description
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}
		if req.ExpiresAt != nil {
			updates["expires_at"] = *req.ExpiresAt
		}

		if err := db.Model(&promo).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar promoción"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": promo})
	}
}

func DeletePromotion(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		result := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).Delete(&models.Promotion{})
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "promoción eliminada"})
	}
}

func PromotionSuggestions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		sevenDaysFromNow := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
		today := time.Now().Format("2006-01-02")

		var expiring []models.Product
		db.Where("tenant_id = ? AND is_available = true AND expiry_date IS NOT NULL AND expiry_date <= ? AND expiry_date >= ?",
			tenantID, sevenDaysFromNow, today).
			Order("expiry_date ASC").
			Find(&expiring)

		fiveDaysAgo := time.Now().AddDate(0, 0, -5)
		var lowRotation []models.Product
		db.Where("tenant_id = ? AND is_available = true AND stock > 0 AND updated_at < ?",
			tenantID, fiveDaysAgo).
			Order("updated_at ASC").
			Limit(10).
			Find(&lowRotation)

		type Suggestion struct {
			ProductUUID    string  `json:"product_uuid"`
			ProductName    string  `json:"product_name"`
			CurrentPrice   float64 `json:"current_price"`
			SuggestedPrice float64 `json:"suggested_price"`
			Reason         string  `json:"reason"`
			Urgency        int     `json:"urgency"`
			PotentialLoss  float64 `json:"potential_loss"`
			Description    string  `json:"description"`
		}

		var suggestions []Suggestion

		for _, p := range expiring {
			minPrice := p.PurchasePrice * 1.05
			suggestedPrice := math.Ceil(minPrice/50) * 50
			if suggestedPrice >= p.Price {
				suggestedPrice = p.Price * 0.8
				suggestedPrice = math.Ceil(suggestedPrice/50) * 50
			}

			daysLeft := 0
			if p.ExpiryDate != nil {
				expDate, err := time.Parse("2006-01-02", *p.ExpiryDate)
				if err == nil {
					daysLeft = int(time.Until(expDate).Hours() / 24)
				}
			}

			suggestions = append(suggestions, Suggestion{
				ProductUUID:    p.ID,
				ProductName:    p.Name,
				CurrentPrice:   p.Price,
				SuggestedPrice: suggestedPrice,
				Reason:         "por_vencer",
				Urgency:        7 - daysLeft,
				PotentialLoss:  p.PurchasePrice * float64(p.Stock),
				Description: fmt.Sprintf("%s: Vence en %d días. Bajar de $%.0f a $%.0f para no perder la plata.",
					p.Name, daysLeft, p.Price, suggestedPrice),
			})
		}

		for _, p := range lowRotation {
			suggestedPrice := p.Price * 0.85
			suggestedPrice = math.Ceil(suggestedPrice/50) * 50

			suggestions = append(suggestions, Suggestion{
				ProductUUID:    p.ID,
				ProductName:    p.Name,
				CurrentPrice:   p.Price,
				SuggestedPrice: suggestedPrice,
				Reason:         "baja_rotacion",
				Urgency:        3,
				PotentialLoss:  p.PurchasePrice * float64(p.Stock),
				Description: fmt.Sprintf("%s: No se vende hace más de 5 días. Bajar de $%.0f a $%.0f.",
					p.Name, p.Price, suggestedPrice),
			})
		}

		c.JSON(http.StatusOK, gin.H{"data": suggestions})
	}
}

func ApplyPromoToPOS(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PromotionUUID string `json:"promotion_uuid" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var promo models.Promotion
		if err := db.Where("id = ? AND tenant_id = ? AND is_active = true", req.PromotionUUID, tenantID).
			First(&promo).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
			return
		}

		// Combo promotions don't carry a single product_uuid — there's
		// nothing to "apply to POS" as a price override, so refuse
		// early instead of dereferencing a nil pointer.
		if promo.ProductUUID == nil || *promo.ProductUUID == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "esta promoción es un combo; no se puede aplicar como precio único al POS",
			})
			return
		}

		if err := db.Model(&models.Product{}).
			Where("id = ? AND tenant_id = ?", *promo.ProductUUID, tenantID).
			Update("price", promo.PromoPrice).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al aplicar precio"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"product_uuid": *promo.ProductUUID,
				"new_price":    promo.PromoPrice,
				"old_price":    promo.OrigPrice,
			},
		})
	}
}

// PromoBannerPromptInput gathers every piece of commercial context
// the AI image model needs to render a READABLE promo banner. Lives
// in its own struct so the handler can validate it, the prompt
// builder can assemble it deterministically, and unit tests can
// exercise it without HTTP.
type PromoBannerPromptInput struct {
	// Legacy inputs (V1). Still used as fallbacks when V2 is missing.
	PromoName    string
	Products     []string
	DiscountText string
	Tone         string

	// V2 — value-proposition injection. See BuildPromoBannerPrompt.
	TenantName     string
	ComboTitle     string
	NormalPriceStr string
	PromoPriceStr  string
	DiscountStr    string
	SavingsStr     string

	// V3 — image sourcing. Tells the prompt whether the model should
	// invent the product visuals (AI_GENERATED) or treat the N images
	// passed alongside this prompt as the SOURCE OF TRUTH for the
	// products (CATALOG_PHOTOS). NumRefImages is how many images
	// actually made it through the fetch step.
	ImageSourceType string
	NumRefImages    int
}

// BuildPromoBannerPrompt assembles the imperative, typographically
// strict system prompt we feed to Gemini's image model.
//
// Why a dedicated builder:
//  1. Retail banner generation is THE product differentiator of the
//     Marketing Hub — if the prompt regresses, the feature dies.
//  2. Gemini image models tend to render decorative text as gibberish
//     unless the prompt pins down EXACT glyph strings, hierarchy and
//     size ratios. The builder enforces that contract.
//  3. Keeping prompt assembly pure lets us unit-test that every
//     commercial string (price, savings, brand) is in the prompt
//     verbatim and no inputs get silently dropped.
//
// If V2 inputs are missing we gracefully fall back to a V1-compatible
// prompt (less rich but still better than nothing) — that way a
// rolling deploy of an old Flutter client does not break generation.
func BuildPromoBannerPrompt(in PromoBannerPromptInput) string {
	tone := strings.TrimSpace(in.Tone)
	if tone == "" {
		tone = "vibrante"
	}
	productsStr := strings.Join(in.Products, ", ")
	discountText := strings.TrimSpace(in.DiscountText)
	if discountText == "" {
		discountText = "¡PROMO ESPECIAL!"
	}

	promoName := firstNonEmpty(in.ComboTitle, in.PromoName, "Promoción especial")
	tenant := strings.TrimSpace(in.TenantName)
	normalPrice := strings.TrimSpace(in.NormalPriceStr)
	promoPrice := strings.TrimSpace(in.PromoPriceStr)
	discountLabel := firstNonEmpty(in.DiscountStr, discountText)
	savings := strings.TrimSpace(in.SavingsStr)

	// If no V2 financial data arrived, produce a V1-equivalent prompt
	// so clients on the previous payload shape keep working. This
	// branch will disappear once the Flutter rollout is at 100 %.
	if normalPrice == "" && promoPrice == "" && savings == "" {
		return fmt.Sprintf(
			`Eres un DIRECTOR DE ARTE publicitario para retail colombiano. Tu única tarea es generar UN banner publicitario HORIZONTAL 16:9 de ALTA CALIDAD para esta promoción.

PROMOCIÓN: %s
PRODUCTOS INCLUIDOS: %s
TEXTO DE DESCUENTO PRINCIPAL: %s
TONO VISUAL: %s

REGLAS ESTRICTAS DE COMPOSICIÓN (no violarlas):
1. Formato HORIZONTAL ESTRICTO con aspect ratio 16:9 (paisaje, wide). Nunca cuadrado, nunca vertical — 16:9 exactos con margen mínimo de 8%% a los 4 lados.
2. COMPOSICIÓN DE ANCHO COMPLETO: los productos y el escenario DEBEN aprovechar todo el ancho del rectángulo. NO dejes grandes franjas laterales vacías. Ejemplo: coloca los productos sobre un mantel, board de madera o mesa de restaurante que se extienda DE BORDE A BORDE. El fondo debe ser un entorno (p.ej. cocina / restaurante desenfocado, bodegón con textiles, góndola de supermercado) que rellene naturalmente el 16:9.
3. LAYOUT HORIZONTAL tipo hero-banner: bloque de producto a un lado (≈55%% del ancho) y bloque de tipografía/oferta al otro (≈45%% del ancho), separados por respiración visual. Los elementos respiran horizontalmente — evita el collage apilado vertical.
4. TIPOGRAFÍA embebida: el texto "%s" debe ser el elemento más grande y leerse a la perfección a 640×360 px (tamaño típico de preview en web). Usa fuentes sans-serif bold con altísimo contraste (texto blanco sobre fondo oscuro o viceversa).
5. El nombre de la promoción "%s" debe aparecer como subtítulo, más pequeño pero legible.
6. Estilo VISUAL: fotografía publicitaria realista o ilustración vectorial limpia — NUNCA estilo cartoon infantil. Paleta vibrante pero coherente (naranjas/rojos para urgencia, verdes/azules para frescura).
7. PROHIBIDO inventar texto adicional, logos falsos, mezclar idiomas, o escribir en inglés.
8. PROHIBIDO usar formato cuadrado 1:1 o vertical 9:16 — si la composición sale cuadrada el banner es RECHAZADO.
9. Los productos deben aparecer representados visualmente (fotografía o ilustración fiel) sin deformaciones.
10. Compuesto para el slot "Special Offers" horizontal de un catálogo web (carrusel 16:9), legible en un scroll rápido de Instagram/WhatsApp Status.

Resultado: un banner horizontal 16:9 listo para publicar, nivel agencia creativa.`,
			promoName, productsStr, discountText, tone,
			discountText, promoName,
		)
	}

	// V2 prompt: imperative, with explicit typographic hierarchy and
	// an anti-pattern list. Empty fields are omitted from the
	// hierarchy so the model doesn't invent values for them.
	var hierarchy strings.Builder
	rank := 1
	addLine := func(role, value, note string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fmt.Fprintf(&hierarchy,
			"%d. %s — TEXTO LITERAL: \"%s\". %s\n",
			rank, role, value, note)
		rank++
	}
	addLine(
		"PRECIO PROMO (elemento MÁS grande — ocupa ±35% del alto del banner)",
		promoPrice,
		"Fuente sans-serif ULTRA BOLD, numeral enorme, color de alto contraste. Es el héroe visual.")
	addLine(
		"PRECIO NORMAL TACHADO (2º tamaño)",
		normalPrice,
		"Debe aparecer TACHADO con una línea diagonal gruesa. Color gris medio o rojo apagado. Más pequeño que el precio promo, claramente subordinado.")
	addLine(
		"ETIQUETA DE DESCUENTO (sello circular o cinta)",
		discountLabel,
		"Colócala como un 'sticker' circular o diagonal cinta en esquina superior derecha. Font-weight bold extremo. Esta etiqueta grita la urgencia.")
	addLine(
		"AHORRO EXPLÍCITO",
		savings,
		"Texto pequeño pero destacado cerca del precio promo. Indica la plata concreta que ahorra el cliente.")
	addLine(
		"TÍTULO DEL COMBO",
		promoName,
		"Subtítulo legible, 3º en jerarquía. Centrado encima o debajo del bloque de precios.")
	addLine(
		"NOMBRE DEL NEGOCIO",
		tenant,
		"Aparece pequeño, tipo firma, en esquina inferior. NO debe competir con los precios.")

	// Image-sourcing block. The prompt tone changes dramatically
	// depending on whether we passed real catalogue photos as
	// reference or we're asking Gemini to render the products from
	// scratch. Keeping this in its own block makes it easy to tweak
	// each branch without disturbing the typographic rules.
	var imagingBlock string
	switch {
	case in.ImageSourceType == "CATALOG_PHOTOS" && in.NumRefImages > 0:
		imagingBlock = fmt.Sprintf(`
══════════════════════════════════════════════════════════════════
FUENTE DE IMÁGENES — MODO FOTOS REALES DEL CATÁLOGO:
Las %d imágenes adjuntas a este mensaje son las FOTOS REALES de los productos de este negocio. Son la FUENTE DE VERDAD visual — NO las reemplaces, NO las reinterpretes, NO generes "versiones mejoradas".

REGLAS:
• Recorta cada producto de su foto adjunta (ignora el fondo original) y COMPONLOS juntos dentro del banner, respetando colores, forma de empaque, etiquetas y marcas visibles. La empanada de la foto 1 es LA empanada del banner, no otra.
• Si una foto tiene baja calidad, mejórala con iluminación de estudio PERO preserva la identidad del producto (color, logo, forma).
• Agrupa los productos de forma composicionalmente atractiva (flat-lay o bodegón), dejando espacio para el bloque de precios y el sello de descuento.
• PROHIBIDO generar productos genéricos ("una empanada cualquiera"), PROHIBIDO añadir productos que no estén en las fotos adjuntas.
══════════════════════════════════════════════════════════════════
`, in.NumRefImages)
	default:
		imagingBlock = `
══════════════════════════════════════════════════════════════════
FUENTE DE IMÁGENES — MODO FOTOS APETITOSAS GENERADAS:
No hay fotos reales adjuntas. Genera cada producto desde cero como FOTOGRAFÍA PUBLICITARIA DE ALTA CALIDAD: iluminación cálida tipo foodie-photography, textura real del producto (crujiente, dorado, fresco), enfoque nítido en primer plano. Estilo "Uber Eats / Rappi hero banner". PROHIBIDO ilustración cartoon, render 3D plástico, o pseudo-realismo gomoso.
══════════════════════════════════════════════════════════════════
`
	}

	return fmt.Sprintf(
		`Eres un DIRECTOR DE ARTE publicitario senior para retail colombiano. Tu ÚNICA tarea es generar UN banner promocional HORIZONTAL 16:9 de alta calidad, estilo HERO-BANNER DE CATÁLOGO WEB / GÓNDOLA DE SUPERMERCADO, que comunique una oferta comercial específica con NÚMEROS LEGIBLES.

══════════════════════════════════════════════════════════════════
CONTEXTO COMERCIAL (usa estos datos EXACTOS, no inventes otros):
• Negocio:            %s
• Combo:              %s
• Productos incluidos: %s
• Tono visual:        %s
══════════════════════════════════════════════════════════════════
%s
JERARQUÍA TIPOGRÁFICA OBLIGATORIA (de MÁS grande a menos grande):
%s
══════════════════════════════════════════════════════════════════

REGLAS DE FORMATO (CRÍTICAS — el slot de destino es un carrusel horizontal web "Special Offers"):
F1. ASPECT RATIO OBLIGATORIO: 16:9 STRICT (paisaje / wide / horizontal). Jamás cuadrado 1:1. Jamás vertical 9:16. Jamás 4:3. Si la imagen sale cuadrada el banner es RECHAZADO automáticamente y se regenera.
F2. La imagen DEBE ocupar el 100%% del marco 16:9 sin franjas blancas, letterbox, pillarbox, ni bordes vacíos decorativos. El slot destino usa object-cover — cualquier esquina desperdiciada se recorta.
F3. Resolución objetivo: 1280×720 px (o proporcional). Legible también a 640×360 px (tamaño preview en móvil).

REGLAS DE COMPOSICIÓN HORIZONTAL (UTILIZACIÓN DE ANCHO COMPLETO):
C1. Layout HERO tipo catálogo Rappi / Uber Eats / Amazon hero-banner:
    • Lado izquierdo (±55%% del ancho): BODEGÓN FOTOGRÁFICO de los productos — sobre mantel, board de madera, mesa de restaurante o góndola que SE EXTIENDA DE BORDE A BORDE del rectángulo, no centrado en un cuadrado chiquito.
    • Lado derecho (±45%% del ancho): BLOQUE COMERCIAL — precios, sello de descuento, título. Respira con el fondo, alineación clara.
    • Opcionalmente se puede invertir izquierda/derecha según la tonalidad, pero NUNCA apilar vertical (apilado vertical rompe el 16:9).
C2. El FONDO debe rellenar todo el 16:9 de forma natural: cocina o restaurante colombiano desenfocado, textil de mesa, superficie de madera con textura, góndola de mercado. PROHIBIDO fondo de color plano rodeando un "cuadrado central" — eso recrea la franja blanca que estamos tratando de eliminar.
C3. Los productos se componen HORIZONTALMENTE (uno al lado del otro, flat-lay alargado), nunca uno encima del otro tipo torre vertical.
C4. Safe-zone de 6%% en los 4 lados — ningún texto ni producto toca los bordes, pero el fondo sí llega hasta el borde.

REGLAS DE RENDER TIPOGRÁFICO (CRÍTICAS — violarlas = banner rechazado):
A. Los valores de precio se renderizan EXACTAMENTE como están escritos arriba, INCLUYENDO el símbolo "$", el punto de miles y cualquier sufijo. No cambies "$8.100" por "$8100" ni lo traduzcas.
B. La tipografía DEBE ser sans-serif geométrica bold (estilo Inter/Montserrat/Poppins), con kerning limpio y SIN deformar los caracteres. Los números deben verse tan legibles como en una publicidad de Éxito o D1.
C. Contraste mínimo 7:1 entre texto de precio y su fondo. Si el fondo es claro, el texto es oscuro; si el fondo es oscuro, el texto es blanco.
D. El precio normal tachado DEBE tener una línea diagonal VISIBLE que lo cruce, señal universal de "antes/después".
E. El sello de descuento debe leerse como etiqueta comercial real (círculo, explosión o cinta), no como texto libre. Colócalo en una ESQUINA del rectángulo (no centrado).

REGLAS DE ESTILO:
S1. Fotografía publicitaria realista de los productos O ilustración vectorial limpia — los productos ocupan ±45%% del área total del banner horizontal, el resto es fondo + copy comercial.
S2. Paleta coherente con el tono "%s": vibrante (naranjas/rojos saturados), elegante (negro+dorado), urgente (amarillo+negro contraste). Fondo sólido, gradiente suave o ambiente fotográfico desenfocado — NUNCA fondos ruidosos que compitan con el texto.
S3. Todos los textos en ESPAÑOL colombiano. No mezclar inglés. No inventar textos adicionales (ni "limited time", ni "sale", ni "SALE %%", ni URLs falsas, ni QRs).
S4. Los productos deben aparecer reconocibles y sin deformaciones — fotografía de empaque real.

ANTI-PATRONES EXPLÍCITOS (si el banner cae en uno → es un FALLO):
✗ Entregar imagen CUADRADA 1:1 o VERTICAL — cualquier aspect ratio distinto a 16:9 es inaceptable.
✗ Centrar todos los productos en un cuadrado chiquito en medio del 16:9 dejando franjas laterales vacías o de color plano.
✗ Parecer una "foto de menú" de restaurante con productos flotando sin precios grandes.
✗ Omitir el precio promo o renderizarlo más pequeño que el nombre del producto.
✗ Escribir precios en formato internacional ($8,100.00 USD) en vez del formato local (%s).
✗ Inventar descuentos distintos a "%s" o precios distintos a los dados.
✗ Tipografía script/serif decorativa ilegible en scroll rápido de WhatsApp.
✗ Composición simétrica aburrida estilo flyer corporativo.

OBJETIVO FINAL: Un banner HORIZONTAL 16:9 que llene el slot "Special Offers" del catálogo web sin franjas blancas, con el precio promo tan legible a 640×360 px que el cliente entienda CUÁNTO ahorra en menos de 1 segundo. Nivel agencia creativa colombiana.`,
		firstNonEmpty(tenant, "Tienda"),
		promoName,
		productsStr,
		tone,
		imagingBlock,
		hierarchy.String(),
		tone,
		firstNonEmpty(promoPrice, normalPrice, "$0"),
		discountLabel,
	)
}

// fetchCatalogReferenceImages pulls up to N product photos from the
// tenant's catalogue in parallel and hands them back as Gemini
// reference images. Failures are tolerated silently (best-effort) —
// the banner can still be generated with fewer refs, or fall back
// to AI-only mode upstream.
//
// We cap each download at 3 seconds because banner generation is
// already latency-sensitive (the tendero is staring at a spinner),
// and a single slow CDN shouldn't block the whole pipeline. Images
// that exceed the Gemini inline limit (~7 MB hard / 4 MB soft) are
// dropped so we never build a payload Google will reject.
func fetchCatalogReferenceImages(ctx context.Context, urls []string) []services.ReferenceImage {
	if len(urls) == 0 {
		return nil
	}
	const perImageTimeout = 3 * time.Second
	const maxBytesPerImage = 4 * 1024 * 1024

	results := make([]services.ReferenceImage, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		if strings.TrimSpace(u) == "" {
			continue
		}
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			imgCtx, cancel := context.WithTimeout(ctx, perImageTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(imgCtx, "GET", url, nil)
			if err != nil {
				log.Printf("[BANNER] build catalog request failed (%s): %v", url, err)
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("[BANNER] catalog fetch failed (%s): %v", url, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				log.Printf("[BANNER] catalog fetch %s -> HTTP %d", url, resp.StatusCode)
				return
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytesPerImage+1))
			if err != nil {
				log.Printf("[BANNER] catalog read failed (%s): %v", url, err)
				return
			}
			if len(data) == 0 || len(data) > maxBytesPerImage {
				log.Printf("[BANNER] catalog image rejected (%s): size=%d", url, len(data))
				return
			}
			mime := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(mime, "image/") {
				// Some CDNs return octet-stream even for images; use
				// a safe default. Gemini accepts jpeg/png/webp here.
				mime = "image/jpeg"
			}
			results[idx] = services.ReferenceImage{MimeType: mime, Data: data}
		}(i, u)
	}
	wg.Wait()

	// Compact the slice dropping empties (failed fetches or skipped
	// blank URLs) so the caller can just range over what worked.
	compact := make([]services.ReferenceImage, 0, len(results))
	for _, r := range results {
		if len(r.Data) > 0 {
			compact = append(compact, r)
		}
	}
	return compact
}

// firstNonEmpty helper lives in payments.go and is reused here.
