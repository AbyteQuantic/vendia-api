package handlers

import (
	"fmt"
	"math"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListPromotions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var promotions []models.Promotion
		if err := db.Where("tenant_id = ? AND is_active = true", tenantID).
			Order("created_at DESC").
			Find(&promotions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener promociones"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": promotions})
	}
}

func CreatePromotion(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID          string  `json:"id"`
		ProductUUID string  `json:"product_uuid" binding:"required"`
		PromoPrice  float64 `json:"promo_price"  binding:"required,gt=0"`
		PromoType   string  `json:"promo_type"`
		Description string  `json:"description"`
		ExpiresAt   *string `json:"expires_at"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", req.ProductUUID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		promoType := req.PromoType
		if promoType == "" {
			promoType = "discount"
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
