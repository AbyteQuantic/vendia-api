package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// onlineOrderStatusAllowed defines the state machine for pedido web.
// "pending" is the default on create; "accepted" / "rejected" are
// the first tendero decision; "completed" closes the order once the
// customer picked it up. We keep the previous lowercase vocabulary so
// existing rows + Flutter screens (catalog_virtual_screen) keep
// working — the brief's Spanish uppercase ("NUEVO", "ACEPTADO", ...)
// is a UI-layer mapping, not a storage change.
var onlineOrderStatusAllowed = map[string]struct{}{
	"pending":   {},
	"accepted":  {},
	"rejected":  {},
	"completed": {},
}

// defaultBranchForTenant returns the oldest active branch for a
// tenant, or an empty pointer when the tenant has no branches yet
// (mono-sede pre-Phase-5). Shared between the public order handler
// and any future admin flow that needs the same tie-breaker.
func defaultBranchForTenant(db *gorm.DB, tenantID string) *string {
	var row models.Branch
	if err := db.Where("tenant_id = ? AND is_active = true AND deleted_at IS NULL", tenantID).
		Order("created_at ASC").
		First(&row).Error; err != nil {
		return nil
	}
	id := row.ID
	return &id
}

// PublicCreateOnlineOrder creates an order from the public catalog.
// Two routes hit this handler:
//
//	POST /api/v1/store/:slug/online-order           (legacy)
//	POST /api/v1/public/catalog/:slug/orders        (brief)
//
// Both are public (no auth). The slug resolves the tenant; the
// active branch is attached so the KDS on that sede sees the
// pedido immediately. Phone is accepted empty for web orders that
// only capture a name.
func PublicCreateOnlineOrder(db *gorm.DB) gin.HandlerFunc {
	type ItemReq struct {
		ProductID string  `json:"product_id"`
		Name      string  `json:"name"`
		Quantity  int     `json:"quantity"`
		Price     float64 `json:"price"`
	}
	type Request struct {
		CustomerName    string    `json:"customer_name" binding:"required"`
		CustomerPhone   string    `json:"customer_phone"`
		DeliveryType    string    `json:"delivery_type"`
		PaymentMethod   string    `json:"payment_method"`
		PaymentMethodID string    `json:"payment_method_id"`
		Items           []ItemReq `json:"items" binding:"required,min=1"`
		Notes           string    `json:"notes"`
		// AcceptedTerms carries the Habeas-Data checkbox from the
		// public catalogue. Only `true` triggers a consent flip on
		// the Customer row (see upsertCustomerFromOrder). Omitted or
		// `false` keeps the row in its prior state — we never revoke
		// consent implicitly here.
		AcceptedTerms bool `json:"accepted_terms"`
	}

	return func(c *gin.Context) {
		slug := c.Param("slug")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var total float64
		for _, item := range req.Items {
			total += item.Price * float64(item.Quantity)
		}

		delivery := req.DeliveryType
		if delivery == "" {
			delivery = "pickup"
		}

		paymentMethodID := strings.TrimSpace(req.PaymentMethodID)
		if paymentMethodID != "" && !models.IsValidUUID(paymentMethodID) {
			// Silently drop a bad id — the free-form name still rides
			// through, and we don't want a malformed UUID from a
			// client to reject the order outright.
			paymentMethodID = ""
		}

		itemsJSON, _ := json.Marshal(req.Items)

		order := models.OnlineOrder{
			TenantID:        tenant.ID,
			BranchID:        defaultBranchForTenant(db, tenant.ID),
			CustomerName:    req.CustomerName,
			CustomerPhone:   req.CustomerPhone,
			DeliveryType:    delivery,
			PaymentMethod:   strings.TrimSpace(req.PaymentMethod),
			PaymentMethodID: paymentMethodID,
			Status:          "pending",
			TotalAmount:     total,
			Items:           string(itemsJSON),
			Notes:           req.Notes,
		}

		if err := db.Create(&order).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al crear pedido",
				"detail": err.Error(),
			})
			return
		}

		// CRM upsert is best-effort — we already persisted the order,
		// so if the customers table has a hiccup we still want the
		// pedido to land in the KDS. Log the error silently (via the
		// returned value) but don't fail the request.
		_, _ = upsertCustomerFromOrder(
			db,
			tenant.ID,
			req.CustomerName,
			req.CustomerPhone,
			req.AcceptedTerms,
		)

		// Create notification for the tenant
		CreateNotification(db, tenant.ID,
			"Nuevo pedido en línea",
			fmt.Sprintf("%s pidió por $%.0f (%s)", req.CustomerName, total, delivery),
			"online_order",
		)

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"order_id": order.ID,
				"total":    total,
				"status":   order.Status,
			},
		})
	}
}

// ListOnlineOrders returns orders for the tenant. Supports:
//
//	?status=pending         — filter by a single state
//	?branch_id=<uuid>       — narrow to one sede (Phase-6 isolation)
//	(JWT's workspace branch) — automatic filter when the caller has
//	                          a sede-scoped token
//
// When the tenant is mono-sede and no branch scope applies, every
// pedido for the tenant returns as before (backward compat).
func ListOnlineOrders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		status := strings.TrimSpace(c.DefaultQuery("status", ""))

		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{"error": "branch_not_owned"})
			return
		}

		query := db.Where("tenant_id = ?", tenantID)
		query = ApplyBranchScope(query, scope)
		if status != "" {
			query = query.Where("status = ?", status)
		}

		var orders []models.OnlineOrder
		query.Order("created_at DESC").Limit(50).Find(&orders)

		c.JSON(http.StatusOK, gin.H{"data": orders})
	}
}

// UpdateOnlineOrderStatus changes order status. Whitelist the target
// state so a typo from the client doesn't wedge an order into an
// unreachable value (before this guard the field was set verbatim).
func UpdateOnlineOrderStatus(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Status string `json:"status" binding:"required"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		orderID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		status := strings.ToLower(strings.TrimSpace(req.Status))
		if _, ok := onlineOrderStatusAllowed[status]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "estado no permitido",
				"allowed": []string{"pending", "accepted", "rejected", "completed"},
			})
			return
		}

		result := db.Model(&models.OnlineOrder{}).
			Where("id = ? AND tenant_id = ?", orderID, tenantID).
			Update("status", status)

		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "estado actualizado"})
	}
}
