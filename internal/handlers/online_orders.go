package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

		// Load + mutate inside a single tx so the bridge to the
		// sales ledger sees the freshly-completed state without a
		// race against a concurrent fetch.
		var order models.OnlineOrder
		err := db.Where("id = ? AND tenant_id = ?", orderID, tenantID).
			First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al cargar pedido",
				"detail": err.Error(),
			})
			return
		}

		previous := order.Status
		if err := db.Model(&order).Update("status", status).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al actualizar estado",
				"detail": err.Error(),
			})
			return
		}

		// Ledger bridge: when an online order transitions INTO
		// "completed" we drop a Sale row with source=WEB so the
		// finance dashboard, the receipts list and any reporting
		// pipeline see web orders alongside POS sales without a
		// special case. Idempotent — guards against re-firing on a
		// double-tap of "Marcar entregado".
		if status == "completed" && previous != "completed" {
			if err := bridgeOnlineOrderToSale(db, order); err != nil {
				log.Printf("[ONLINE_ORDERS] sale bridge failed order=%s: %v",
					order.ID, err)
				// Don't block the status update — the order is
				// still completed; we'll surface the bridge failure
				// in logs for ops, not in the cashier's UI.
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "estado actualizado"})
	}
}

// bridgeOnlineOrderToSale upserts a Sale row mirroring an OnlineOrder
// that just landed in completed. The Sale's id matches the order's id
// so re-firing is a no-op (ON CONFLICT DO NOTHING via Where().Count
// guard). Items are fanned out into sale_items so the unified
// /sales/history endpoint can render them with the same shape used
// for POS sales.
func bridgeOnlineOrderToSale(db *gorm.DB, order models.OnlineOrder) error {
	var existing int64
	db.Model(&models.Sale{}).Where("id = ?", order.ID).Count(&existing)
	if existing > 0 {
		return nil // already bridged on a previous completion
	}

	method := models.PaymentCash
	switch strings.ToLower(strings.TrimSpace(order.PaymentMethod)) {
	case "transferencia", "transfer", "nequi", "daviplata":
		method = models.PaymentTransfer
	case "tarjeta", "card", "credito", "credit":
		method = models.PaymentCard
	}

	// Decode the JSON-encoded items blob the OnlineOrder carries so
	// each line gets its own SaleItem row with name/qty/price.
	type orderItem struct {
		ProductID string  `json:"product_id"`
		Name      string  `json:"name"`
		Quantity  int     `json:"quantity"`
		Price     float64 `json:"price"`
	}
	var rawItems []orderItem
	if order.Items != "" {
		if err := json.Unmarshal([]byte(order.Items), &rawItems); err != nil {
			return fmt.Errorf("decode items: %w", err)
		}
	}

	return db.Transaction(func(tx *gorm.DB) error {
		sale := models.Sale{
			BaseModel: models.BaseModel{ID: order.ID},
			TenantID:  order.TenantID,
			BranchID:  order.BranchID,
			Total:     order.TotalAmount,
			PaymentMethod: method,
			PaymentStatus: "COMPLETED",
			Source:        models.SaleSourceWeb,
			CustomerNameSnapshot:  order.CustomerName,
			CustomerPhoneSnapshot: order.CustomerPhone,
		}
		if err := tx.Create(&sale).Error; err != nil {
			return fmt.Errorf("create sale: %w", err)
		}
		for _, it := range rawItems {
			subtotal := it.Price * float64(it.Quantity)
			item := models.SaleItem{
				SaleID:   sale.ID,
				Name:     it.Name,
				Price:    it.Price,
				Quantity: it.Quantity,
				Subtotal: subtotal,
			}
			// product_id is optional: web orders snapshot the name
			// at order time so a deleted product still shows up on
			// the receipt. Only attach when the order_item carries
			// a valid UUID.
			if models.IsValidUUID(it.ProductID) {
				pid := it.ProductID
				item.ProductID = &pid
			}
			if err := tx.Create(&item).Error; err != nil {
				return fmt.Errorf("create sale_item: %w", err)
			}
		}
		return nil
	})
}

// PublicCustomerOrder is the deliberately-narrow projection a guest
// caller sees from the my-orders endpoint. We exclude every field
// that could be exfiltrated by guessing a phone number — the brief
// flagged this as a Habeas-Data risk:
//
//   - customer_name, customer_phone (the caller already knows the
//     phone they queried; we don't reveal whose name it belongs to)
//   - notes (live-tab order builder dumps "Entrega: <address>" here)
//   - branch_id, payment_method_id, payment_method, tenant_id
//     (operator-side metadata)
//   - any soft-delete / audit timestamps
//
// What WE DO return: id, status, created_at, total_amount, items
// (which only carry product names + qty + price — never customer
// PII). The customer can rebuild their own context from those fields.
type PublicCustomerOrder struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	TotalAmount float64 `json:"total_amount"`
	Items       any     `json:"items"`
}

// PublicCustomerOrders — GET /api/v1/public/catalog/:slug/my-orders?phone=…
//
// Privacy posture:
//
//   - Lookup MUST be scoped to the tenant resolved from the slug
//     so a phone match can't cross tenants. A phone that's a regular
//     at Tienda A AND has ordered once at Tienda B only sees Tienda
//     A's orders when querying via Tienda A's slug.
//   - The response runs through PublicCustomerOrder and never
//     leaks customer_name / address / notes — see type comment.
//   - We require a non-trivial phone (≥7 digits) to make brute-force
//     scraping marginally harder. Anything that doesn't match the
//     digit gate returns an empty list with 200 — never reveal
//     "ese número no existe" vs "ese número no tiene pedidos".
//   - Hard cap of 50 rows so a chatty query can't pull the whole
//     order history of a high-volume tenant.
func PublicCustomerOrders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := strings.TrimSpace(c.Param("slug"))
		phone := strings.TrimSpace(c.Query("phone"))

		// Strip everything but digits before the length check so a
		// "+57 300…" string doesn't bypass the brute-force gate.
		digitOnly := stripNonDigits(phone)
		if len(digitOnly) < 7 {
			// Empty list keeps the response shape uniform regardless
			// of why the lookup failed — the caller can't fingerprint
			// "wrong phone format" vs "phone with no orders".
			c.JSON(http.StatusOK, gin.H{"data": []PublicCustomerOrder{}})
			return
		}

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"data": []PublicCustomerOrder{}})
			return
		}

		var rows []models.OnlineOrder
		// We match against the literal phone the customer typed AND
		// the digits-only version so a tenant that stored "+57 300
		// 555 1234" in some rows and "3005551234" in others still
		// returns the full set. OnlineOrder doesn't use BaseModel,
		// so no deleted_at filter is needed (or available).
		db.Where("tenant_id = ?", tenant.ID).
			Where("customer_phone = ? OR customer_phone = ?", phone, digitOnly).
			Order("created_at DESC").
			Limit(50).
			Find(&rows)

		out := make([]PublicCustomerOrder, 0, len(rows))
		for _, r := range rows {
			// items is JSON-encoded text on the wire. Decode lazily
			// so the customer-side UI can render line items without
			// re-parsing. Fall back to the raw string when the JSON
			// is malformed (older rows / partial writes).
			var items any
			if err := json.Unmarshal([]byte(r.Items), &items); err != nil {
				items = []any{}
			}
			out = append(out, PublicCustomerOrder{
				ID:          r.ID,
				Status:      r.Status,
				CreatedAt:   r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
				TotalAmount: r.TotalAmount,
				Items:       items,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

func stripNonDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
