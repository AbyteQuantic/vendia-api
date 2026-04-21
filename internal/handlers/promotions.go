package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
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

			txErr := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&promo).Error; err != nil {
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
						return err
					}
					promo.Items = append(promo.Items, item)
				}
				return nil
			})
			if txErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear promoción combo"})
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
			ProductUUID: req.ProductUUID,
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear promoción"})
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
func GenerateMarketingBanner(geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	type Request struct {
		PromoName    string   `json:"promo_name"    binding:"required"`
		Products     []string `json:"products"      binding:"required,min=1"`
		DiscountText string   `json:"discount_text"`
		Tone         string   `json:"tone"` // "vibrante" | "elegante" | "urgente" | ""
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

		tone := strings.TrimSpace(req.Tone)
		if tone == "" {
			tone = "vibrante"
		}
		productsStr := strings.Join(req.Products, ", ")
		discount := strings.TrimSpace(req.DiscountText)
		if discount == "" {
			discount = "¡PROMO ESPECIAL!"
		}

		prompt := fmt.Sprintf(
			`Eres un DIRECTOR DE ARTE publicitario para retail colombiano. Tu única tarea es generar UN banner publicitario cuadrado 1:1 de ALTA CALIDAD para esta promoción.

PROMOCIÓN: %s
PRODUCTOS INCLUIDOS: %s
TEXTO DE DESCUENTO PRINCIPAL: %s
TONO VISUAL: %s

REGLAS ESTRICTAS DE COMPOSICIÓN (no violarlas):
1. Formato CUADRADO 1:1 con margen mínimo de 10%% a los 4 lados.
2. TIPOGRAFÍA embebida: el texto "%s" debe ser el elemento más grande y leerse a la perfección a 320×320 px. Usa fuentes sans-serif bold con altísimo contraste (texto blanco sobre fondo oscuro o viceversa).
3. El nombre de la promoción "%s" debe aparecer como subtítulo, más pequeño pero legible.
4. Estilo VISUAL: fotografía publicitaria realista o ilustración vectorial limpia — NUNCA estilo cartoon infantil. Paleta vibrante pero coherente (naranjas/rojos para urgencia, verdes/azules para frescura).
5. PROHIBIDO inventar texto adicional, logos falsos, mezclar idiomas, o escribir en inglés.
6. Los productos deben aparecer representados visualmente (fotografía o ilustración fiel) sin deformaciones.
7. Compuesto para redes sociales colombianas: legible en un scroll rápido de Instagram/WhatsApp Status.

Resultado: un banner listo para publicar, nivel agencia creativa.`,
			req.PromoName, productsStr, discount, tone, discount, req.PromoName,
		)

		ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
		defer cancel()

		imageBytes, err := geminiSvc.GeneratePromoBanner(ctx, prompt)
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

		if err := db.Model(&models.Product{}).
			Where("id = ? AND tenant_id = ?", promo.ProductUUID, tenantID).
			Update("price", promo.PromoPrice).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al aplicar precio"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"product_uuid": promo.ProductUUID,
				"new_price":    promo.PromoPrice,
				"old_price":    promo.OrigPrice,
			},
		})
	}
}
