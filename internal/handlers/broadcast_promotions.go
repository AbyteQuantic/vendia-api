// Spec: specs/033-difusion-promociones/spec.md
package handlers

import (
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

// ── F033 — broadcast promotions CRUD ────────────────────────────────────────
//
// This is the customer-broadcast promotions module. It is deliberately
// separate from the legacy combo-promo handlers in promotions.go: those
// drive the public catalog carousel + POS price override, this one
// drives segmented WhatsApp/link campaigns to identified customers
// (F030). The routes live under /broadcast-promotions so there is no
// collision with the legacy /promotions paths.

// loadPromotion fetches a tenant-scoped BroadcastPromotion by id, writing
// a 404 and returning ok=false when it is missing or belongs to another
// tenant (Constitución Art. III — a cross-tenant id is indistinguishable
// from a missing one).
func loadPromotion(c *gin.Context, db *gorm.DB, preloadItems bool) (models.BroadcastPromotion, bool) {
	tenantID := middleware.GetTenantID(c)
	id := c.Param("id")

	var promo models.BroadcastPromotion
	q := db.Where("id = ? AND tenant_id = ?", id, tenantID)
	if preloadItems {
		q = q.Preload("Items")
	}
	if err := q.First(&promo).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "promoción no encontrada"})
		return models.BroadcastPromotion{}, false
	}
	return promo, true
}

// promoItemReq is one product-on-offer line in a create/update payload.
// Exactly one of promo_price / discount_pct is expected; the handler
// does not reject a payload that sets both — the Flutter form guarantees
// the contract — but the model documents the convention.
type promoItemReq struct {
	ProductID   string   `json:"product_id"`
	PromoPrice  *float64 `json:"promo_price"`
	DiscountPct *float64 `json:"discount_pct"`
}

// ListBroadcastPromotions returns the tenant's broadcast campaigns,
// newest first, each with a lightweight metrics block (audience size,
// sent count, visit count). Optional ?status= filter: active | expired |
// draft. GET /api/v1/broadcast-promotions
func ListBroadcastPromotions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		statusFilter := strings.TrimSpace(c.Query("status"))
		now := time.Now().UTC()

		base := db.Model(&models.BroadcastPromotion{}).
			Where("tenant_id = ?", tenantID)
		switch statusFilter {
		case "active":
			base = base.Where("is_active = ? AND valid_from <= ? AND valid_until >= ?", true, now, now)
		case "expired":
			base = base.Where("valid_until < ?", now)
		case "draft":
			base = base.Where("valid_from > ?", now)
		case "":
			// no extra filter
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "filtro de estado inválido"})
			return
		}

		var promos []models.BroadcastPromotion
		if err := base.Preload("Items").
			Order("created_at DESC").Find(&promos).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener promociones"})
			return
		}

		// Una sola query agregada (antes 2 COUNT por promo = N+1). db.Model
		// conserva el scope soft-delete (deleted_at IS NULL) en el WHERE, así
		// que audience y sent lo respetan. Audit 2026-06-24.
		ids := make([]string, len(promos))
		for i, p := range promos {
			ids[i] = p.ID
		}
		type aggRow struct {
			PromotionID string
			Audience    int64
			Sent        int64
		}
		metricsByID := make(map[string]aggRow, len(promos))
		if len(ids) > 0 {
			var rows []aggRow
			db.Model(&models.BroadcastPromotionDelivery{}).
				Select("promotion_id, COUNT(*) AS audience, COUNT(*) FILTER (WHERE status = ?) AS sent", models.PromotionDeliverySent).
				Where("promotion_id IN ?", ids).
				Group("promotion_id").
				Scan(&rows)
			for _, r := range rows {
				metricsByID[r.PromotionID] = r
			}
		}

		out := make([]gin.H, 0, len(promos))
		for _, p := range promos {
			m := metricsByID[p.ID]
			out = append(out, gin.H{
				"promotion": p,
				"metrics": gin.H{
					"audience_count": m.Audience,
					"sent_count":     m.Sent,
					"visit_count":    p.VisitCount,
				},
			})
		}

		c.JSON(http.StatusOK, gin.H{"data": promos, "items": out})
	}
}

// CreateBroadcastPromotion creates a campaign. valid_until must be after
// valid_from; an empty title is rejected. A fresh UUID v4 public_token is
// always generated server-side so the public link is unguessable.
// POST /api/v1/broadcast-promotions
func CreateBroadcastPromotion(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		ID              string         `json:"id"`
		Title           string         `json:"title"`
		Description     string         `json:"description"`
		ImageURL        string         `json:"image_url"`
		CouponCode      string         `json:"coupon_code"`
		MessageTemplate string         `json:"message_template"`
		ValidFrom       *time.Time     `json:"valid_from"`
		ValidUntil      *time.Time     `json:"valid_until"`
		ScheduledFor    *time.Time     `json:"scheduled_for"`
		LegacyComboID   string         `json:"legacy_combo_id"`
		Items           []promoItemReq `json:"items"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if strings.TrimSpace(req.Title) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el título de la promoción es obligatorio"})
			return
		}

		// Default vigencia: 7 days from now if either bound is missing.
		now := time.Now().UTC()
		validFrom := now
		if req.ValidFrom != nil {
			validFrom = *req.ValidFrom
		}
		validUntil := now.AddDate(0, 0, 7)
		if req.ValidUntil != nil {
			validUntil = *req.ValidUntil
		}
		if !validUntil.After(validFrom) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la fecha de fin debe ser posterior a la fecha de inicio",
			})
			return
		}

		promo := models.BroadcastPromotion{
			TenantID:        tenantID,
			Title:           strings.TrimSpace(req.Title),
			Description:     req.Description,
			ImageURL:        req.ImageURL,
			CouponCode:      strings.TrimSpace(req.CouponCode),
			MessageTemplate: req.MessageTemplate,
			ValidFrom:       validFrom,
			ValidUntil:      validUntil,
			ScheduledFor:    req.ScheduledFor,
			PublicToken:     uuid.NewString(),
			LegacyComboID:   middleware.UUIDPtr(req.LegacyComboID),
			IsActive:        true,
		}
		if req.ID != "" {
			promo.ID = req.ID
		}

		txErr := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&promo).Error; err != nil {
				return err
			}
			for _, it := range req.Items {
				if strings.TrimSpace(it.ProductID) == "" {
					continue
				}
				item := models.BroadcastPromotionItem{
					PromotionID: promo.ID,
					ProductID:   it.ProductID,
					PromoPrice:  it.PromoPrice,
					DiscountPct: it.DiscountPct,
				}
				if err := tx.Create(&item).Error; err != nil {
					return err
				}
				promo.Items = append(promo.Items, item)
			}
			return nil
		})
		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al crear promoción",
				"detail": txErr.Error(),
			})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": promo})
	}
}

// GetBroadcastPromotion returns one campaign with items + metrics
// (audience size / sent / visits) and the per-delivery log.
// GET /api/v1/broadcast-promotions/:id
func GetBroadcastPromotion(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		promo, ok := loadPromotion(c, db, true)
		if !ok {
			return
		}

		var deliveries []models.BroadcastPromotionDelivery
		db.Preload("Customer").
			Where("promotion_id = ?", promo.ID).
			Order("created_at ASC").
			Find(&deliveries)

		var sent int64
		for _, d := range deliveries {
			if d.Status == models.PromotionDeliverySent {
				sent++
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"promotion":  promo,
				"deliveries": deliveries,
				"metrics": gin.H{
					"audience_count": len(deliveries),
					"sent_count":     sent,
					"visit_count":    promo.VisitCount,
				},
			},
		})
	}
}

// UpdateBroadcastPromotion partially updates a campaign. It is REFUSED
// (409) once the campaign has at least one delivery in `sent` status:
// what was already broadcast cannot be edited — the owner must create a
// new campaign instead (Spec F033 plan D2 / T-06).
// PATCH /api/v1/broadcast-promotions/:id
func UpdateBroadcastPromotion(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		Title           *string    `json:"title"`
		Description     *string    `json:"description"`
		ImageURL        *string    `json:"image_url"`
		CouponCode      *string    `json:"coupon_code"`
		MessageTemplate *string    `json:"message_template"`
		ValidFrom       *time.Time `json:"valid_from"`
		ValidUntil      *time.Time `json:"valid_until"`
		ScheduledFor    *time.Time `json:"scheduled_for"`
		IsActive        *bool      `json:"is_active"`
	}

	return func(c *gin.Context) {
		promo, ok := loadPromotion(c, db, false)
		if !ok {
			return
		}

		var sentCount int64
		if err := db.Model(&models.BroadcastPromotionDelivery{}).
			Where("promotion_id = ? AND status = ?", promo.ID, models.PromotionDeliverySent).
			Count(&sentCount).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al validar promoción"})
			return
		}
		if sentCount > 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error": "esta promoción ya tiene envíos realizados; crea una nueva en vez de editarla",
			})
			return
		}

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Title != nil {
			if strings.TrimSpace(*req.Title) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el título no puede quedar vacío"})
				return
			}
			updates["title"] = strings.TrimSpace(*req.Title)
		}
		if req.Description != nil {
			updates["description"] = *req.Description
		}
		if req.ImageURL != nil {
			updates["image_url"] = *req.ImageURL
		}
		if req.CouponCode != nil {
			updates["coupon_code"] = strings.TrimSpace(*req.CouponCode)
		}
		if req.MessageTemplate != nil {
			updates["message_template"] = *req.MessageTemplate
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}
		if req.ScheduledFor != nil {
			updates["scheduled_for"] = *req.ScheduledFor
			// Re-arming the schedule must clear the "push already sent"
			// marker so the job notifies the owner for the new time.
			updates["schedule_push_sent"] = false
		}

		// Vigencia edits: validate the resulting window stays coherent.
		newFrom := promo.ValidFrom
		newUntil := promo.ValidUntil
		if req.ValidFrom != nil {
			newFrom = *req.ValidFrom
			updates["valid_from"] = newFrom
		}
		if req.ValidUntil != nil {
			newUntil = *req.ValidUntil
			updates["valid_until"] = newUntil
		}
		if (req.ValidFrom != nil || req.ValidUntil != nil) && !newUntil.After(newFrom) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la fecha de fin debe ser posterior a la fecha de inicio",
			})
			return
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no hay campos para actualizar"})
			return
		}

		if err := db.Model(&models.BroadcastPromotion{}).
			Where("id = ?", promo.ID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar promoción"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "promoción actualizada"})
	}
}

// DeleteBroadcastPromotion removes a campaign and cascades its items and
// deliveries inside a single transaction.
// DELETE /api/v1/broadcast-promotions/:id
func DeleteBroadcastPromotion(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		promo, ok := loadPromotion(c, db, false)
		if !ok {
			return
		}

		txErr := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("promotion_id = ?", promo.ID).
				Delete(&models.BroadcastPromotionItem{}).Error; err != nil {
				return err
			}
			if err := tx.Where("promotion_id = ?", promo.ID).
				Delete(&models.BroadcastPromotionDelivery{}).Error; err != nil {
				return err
			}
			return tx.Delete(&models.BroadcastPromotion{}, "id = ?", promo.ID).Error
		})
		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar promoción"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "promoción eliminada"})
	}
}

// BroadcastPromotionAudience resolves an RFM filter (or a manual id list)
// into the list of customers — with phone — that match. Body:
//
//	{"filter": "frequent"|"vip"|"dormant"|"recent"|"all"|"manual",
//	 "customer_ids": [...]}   // customer_ids only used for "manual"
//
// POST /api/v1/broadcast-promotions/:id/audience
func BroadcastPromotionAudience(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		Filter      string   `json:"filter"`
		CustomerIDs []string `json:"customer_ids"`
	}

	return func(c *gin.Context) {
		// The promotion must exist + belong to the tenant so a caller
		// cannot probe another tenant's customers via a stale promo id.
		promo, ok := loadPromotion(c, db, false)
		if !ok {
			return
		}

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		filter := strings.TrimSpace(strings.ToLower(req.Filter))

		// Manual mode: intersect the requested ids with the tenant's
		// customers that actually have a phone — so a forged id list
		// cannot leak cross-tenant rows or unreachable customers.
		if filter == "manual" {
			if len(req.CustomerIDs) == 0 {
				c.JSON(http.StatusOK, gin.H{"data": []services.AudienceCustomer{}, "meta": gin.H{"count": 0}})
				return
			}
			all, err := services.BuildAudience(db, promo.TenantID, services.AudienceFilterAll, time.Now().UTC())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al construir audiencia"})
				return
			}
			wanted := map[string]struct{}{}
			for _, id := range req.CustomerIDs {
				wanted[id] = struct{}{}
			}
			selected := make([]services.AudienceCustomer, 0, len(req.CustomerIDs))
			for _, a := range all {
				if _, hit := wanted[a.ID]; hit {
					selected = append(selected, a)
				}
			}
			c.JSON(http.StatusOK, gin.H{
				"data": selected,
				"meta": gin.H{"count": len(selected)},
			})
			return
		}

		audience, err := services.BuildAudience(db, promo.TenantID, filter, time.Now().UTC())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": audience,
			"meta": gin.H{"count": len(audience)},
		})
	}
}

// CreateBroadcastDeliveries enqueues one delivery row per customer for
// the given channel. The unique index on (promotion, customer, channel)
// is the anti-duplicate backstop; we use ON CONFLICT DO NOTHING semantics
// (GORM clause.OnConflict) so re-queuing the same audience is idempotent.
// POST /api/v1/broadcast-promotions/:id/deliveries
func CreateBroadcastDeliveries(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		CustomerIDs []string `json:"customer_ids"`
		Channel     string   `json:"channel"`
	}

	return func(c *gin.Context) {
		promo, ok := loadPromotion(c, db, false)
		if !ok {
			return
		}

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		channel := strings.TrimSpace(strings.ToLower(req.Channel))
		if _, valid := models.ValidPromotionChannels[channel]; !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "canal de envío inválido"})
			return
		}
		if len(req.CustomerIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "selecciona al menos un cliente"})
			return
		}

		// Only enqueue customers that belong to this tenant — a forged
		// id cannot create a cross-tenant delivery.
		var validIDs []string
		if err := db.Model(&models.Customer{}).
			Where("tenant_id = ? AND id IN ?", promo.TenantID, req.CustomerIDs).
			Pluck("id", &validIDs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al validar clientes"})
			return
		}
		if len(validIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ningún cliente válido en la selección"})
			return
		}

		// Insert idempotently: a duplicate (promo, customer, channel)
		// is silently skipped instead of erroring, so re-running the
		// queue never spams a customer (plan D2).
		for _, cid := range validIDs {
			delivery := models.BroadcastPromotionDelivery{
				PromotionID: promo.ID,
				CustomerID:  cid,
				Channel:     channel,
				Status:      models.PromotionDeliveryQueued,
			}
			// FirstOrCreate keyed on the unique tuple — portable across
			// Postgres and the SQLite test driver (clause.OnConflict
			// behaves differently per dialect for composite keys).
			db.Where(models.BroadcastPromotionDelivery{
				PromotionID: promo.ID, CustomerID: cid, Channel: channel,
			}).FirstOrCreate(&delivery)
		}

		var deliveries []models.BroadcastPromotionDelivery
		db.Preload("Customer").
			Where("promotion_id = ? AND channel = ?", promo.ID, channel).
			Order("created_at ASC").
			Find(&deliveries)

		c.JSON(http.StatusCreated, gin.H{"data": deliveries})
	}
}

// UpdateBroadcastDelivery marks a delivery `sent` or `skipped` after the
// owner acted in WhatsApp. A `sent` transition stamps sent_at.
// PATCH /api/v1/broadcast-promotions/:id/deliveries/:deliveryId
func UpdateBroadcastDelivery(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		Status string `json:"status"`
	}

	return func(c *gin.Context) {
		promo, ok := loadPromotion(c, db, false)
		if !ok {
			return
		}
		deliveryID := c.Param("deliveryId")

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		status := strings.TrimSpace(strings.ToLower(req.Status))
		if status != models.PromotionDeliverySent && status != models.PromotionDeliverySkipped {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "estado inválido: usa 'sent' o 'skipped'",
			})
			return
		}

		var delivery models.BroadcastPromotionDelivery
		if err := db.Where("id = ? AND promotion_id = ?", deliveryID, promo.ID).
			First(&delivery).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "envío no encontrado"})
			return
		}

		updates := map[string]any{"status": status}
		if status == models.PromotionDeliverySent {
			updates["sent_at"] = time.Now().UTC()
		}
		if err := db.Model(&models.BroadcastPromotionDelivery{}).
			Where("id = ?", delivery.ID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar envío"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "envío actualizado", "status": status})
	}
}
