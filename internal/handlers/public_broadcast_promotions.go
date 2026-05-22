// Spec: specs/033-difusion-promociones/spec.md
package handlers

import (
	"net/http"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// promotionVigenciaStatus reports whether the campaign is showable
// (`active`), still in the future (`not_yet_started`) or over
// (`expired`), based on the lazy comparison at read time (Spec F033
// R5 / AC-05). The public page renders a friendly message for the two
// non-active states instead of hiding the link.
func promotionVigenciaStatus(p models.BroadcastPromotion, now time.Time) string {
	switch {
	case now.Before(p.ValidFrom):
		return "not_yet_started"
	case now.After(p.ValidUntil):
		return "expired"
	default:
		return "active"
	}
}

// GetPublicBroadcastPromotion serves the customer-facing promo page by
// its unguessable public token (Spec F033 AC-05). No JWT — the token is
// the only credential, same pattern as the public quote/fiado links.
//
// The response carries the tenant branding the public page needs, the
// items on offer, and a lazily-computed vigencia status so an expired
// or not-yet-started campaign renders a friendly message rather than a
// 404. The GET also increments visit_count (plan D4 — opening the link
// counts as a visit).
// GET /api/v1/public/broadcast-promotions/:token
func GetPublicBroadcastPromotion(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Param("token")
		if !models.IsValidUUID(token) {
			c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
			return
		}

		var promo models.BroadcastPromotion
		if err := db.Preload("Items").
			Where("public_token = ?", token).
			First(&promo).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
			return
		}

		var tenant models.Tenant
		if err := db.Select("business_name", "logo_url", "phone", "address", "store_slug").
			Where("id = ?", promo.TenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		now := time.Now().UTC()
		status := promotionVigenciaStatus(promo, now)

		// Best-effort visit counter. A failed increment must never fail
		// the page render — the customer still sees the promo.
		_ = db.Model(&models.BroadcastPromotion{}).
			Where("id = ?", promo.ID).
			UpdateColumn("visit_count", gorm.Expr("visit_count + 1")).Error

		storeSlug := ""
		if tenant.StoreSlug != nil {
			storeSlug = *tenant.StoreSlug
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"title":       promo.Title,
				"description": promo.Description,
				"image_url":   promo.ImageURL,
				"coupon_code": promo.CouponCode,
				"valid_from":  promo.ValidFrom,
				"valid_until": promo.ValidUntil,
				"status":      status,
				"items":       promo.Items,
				"business": gin.H{
					"name":       tenant.BusinessName,
					"logo_url":   tenant.LogoURL,
					"phone":      tenant.Phone,
					"address":    tenant.Address,
					"store_slug": storeSlug,
				},
			},
		})
	}
}

// VisitPublicBroadcastPromotion records an explicit visit. When the body
// carries a delivery_id the matching delivery is stamped visited_at so
// the owner's metrics show who opened the link from their WhatsApp
// broadcast (Spec F033 plan D5). Rate-limited at the route level.
// POST /api/v1/public/broadcast-promotions/:token/visit
func VisitPublicBroadcastPromotion(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		DeliveryID string `json:"delivery_id"`
	}

	return func(c *gin.Context) {
		token := c.Param("token")
		if !models.IsValidUUID(token) {
			c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
			return
		}

		var promo models.BroadcastPromotion
		if err := db.Where("public_token = ?", token).First(&promo).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
			return
		}

		// Body is optional — a plain visit ping without a delivery_id is
		// valid (the link shared on a status has no delivery).
		var req request
		_ = c.ShouldBindJSON(&req)

		if err := db.Model(&models.BroadcastPromotion{}).
			Where("id = ?", promo.ID).
			UpdateColumn("visit_count", gorm.Expr("visit_count + 1")).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar la visita"})
			return
		}

		// Mark the delivery only when the id belongs to this promotion —
		// a forged delivery_id from another campaign is ignored.
		if req.DeliveryID != "" && models.IsValidUUID(req.DeliveryID) {
			_ = db.Model(&models.BroadcastPromotionDelivery{}).
				Where("id = ? AND promotion_id = ? AND visited_at IS NULL",
					req.DeliveryID, promo.ID).
				UpdateColumn("visited_at", time.Now().UTC()).Error
		}

		c.JSON(http.StatusOK, gin.H{"message": "visita registrada"})
	}
}
