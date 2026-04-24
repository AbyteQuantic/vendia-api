package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PublicTableSession is a deliberately narrowed projection of an
// OrderTicket. It omits everything that could identify the tenant
// operator (created_by, employee_uuid, branch_id), the session
// token itself (the caller already has it), delivery PII, and any
// fields the cashier might flip via the authenticated API.
//
// Keep the JSON contract flat so the Next.js page can render it
// with a single fetch — we do not want the web client doing a
// join against the catalog just to show "Mesa 1".
type PublicTableSession struct {
	TableLabel      string             `json:"table_label"`
	Status          models.OrderStatus `json:"status"`
	Type            models.OrderType   `json:"type"`
	Total           float64            `json:"total"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	WaiterCalledAt  *time.Time         `json:"waiter_called_at,omitempty"`
	Items           []PublicTableItem  `json:"items"`
	TenantName      string             `json:"tenant_name,omitempty"`
	TenantBrandLogo string             `json:"tenant_brand_logo,omitempty"`
}

type PublicTableItem struct {
	ProductName string  `json:"product_name"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	Emoji       string  `json:"emoji,omitempty"`
	Subtotal    float64 `json:"subtotal"`
}

// GetPublicTableSession serves GET /api/v1/public/table-sessions/:session_token.
//
// Security posture:
//   - Lookup is by session_token only; we never expose the order
//     primary key or the tenant_id in the response.
//   - UUID-parse the token up front to reject trivially malformed
//     inputs without hitting the DB.
//   - Closed tickets (cobrado / cancelado) return 410 Gone so the
//     QR becomes useless after settlement — prevents forever-open
//     links from leaking past the meal.
func GetPublicTableSession(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Param("session_token"))
		if _, err := uuid.Parse(token); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}

		var order models.OrderTicket
		err := db.Preload("Items").
			Where("session_token = ?", token).
			First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al cargar la cuenta",
				"detail": err.Error(),
			})
			return
		}

		if order.Status == models.OrderStatusCobrado ||
			order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusGone, gin.H{
				"error":  "la cuenta ya fue cerrada",
				"status": order.Status,
			})
			return
		}

		// Hydrate the tenant display fields. Missing tenant row is
		// unexpected (FK guarantees it exists) but we degrade to
		// empty strings rather than 500.
		var tenant models.Tenant
		_ = db.Select("business_name", "logo_url").
			Where("id = ?", order.TenantID).
			First(&tenant).Error

		items := make([]PublicTableItem, 0, len(order.Items))
		for _, it := range order.Items {
			items = append(items, PublicTableItem{
				ProductName: it.ProductName,
				Quantity:    it.Quantity,
				UnitPrice:   it.UnitPrice,
				Emoji:       it.Emoji,
				Subtotal:    it.UnitPrice * float64(it.Quantity),
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"data": PublicTableSession{
				TableLabel:      order.Label,
				Status:          order.Status,
				Type:            order.Type,
				Total:           order.Total,
				CreatedAt:       order.CreatedAt,
				UpdatedAt:       order.UpdatedAt,
				WaiterCalledAt:  order.WaiterCalledAt,
				Items:           items,
				TenantName:      tenant.BusinessName,
				TenantBrandLogo: tenant.LogoURL,
			},
		})
	}
}

// CallWaiter serves POST /api/v1/public/table-sessions/:session_token/call-waiter.
//
// Idempotent by design: we rate-limit to one call per 60 s so a
// nervous customer tapping the button five times doesn't flood
// the KDS with notifications. Returns 200 on accept, 429 when
// suppressed so the client can show "ya avisaste hace unos
// segundos" instead of celebrating a silent no-op.
func CallWaiter(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Param("session_token"))
		if _, err := uuid.Parse(token); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}

		var order models.OrderTicket
		err := db.Where("session_token = ?", token).First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al procesar la llamada",
				"detail": err.Error(),
			})
			return
		}

		if order.Status == models.OrderStatusCobrado ||
			order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusGone, gin.H{"error": "la cuenta ya fue cerrada"})
			return
		}

		// Rate-limit: honor the last recorded ping.
		if order.WaiterCalledAt != nil && time.Since(*order.WaiterCalledAt) < time.Minute {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":        "ya avisaste al mesero hace unos segundos",
				"called_at":    order.WaiterCalledAt,
				"retry_after":  60 - int(time.Since(*order.WaiterCalledAt).Seconds()),
			})
			return
		}

		now := time.Now().UTC()
		if err := db.Model(&order).
			Update("waiter_called_at", now).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no pudimos registrar la llamada",
				"detail": err.Error(),
			})
			return
		}

		// Push a notification into the tenant's activity feed so
		// the KDS bell lights up without the cashier needing to
		// refresh the orders screen. Non-fatal on failure — the
		// customer's UI already received the 200 OK.
		label := order.Label
		if label == "" {
			label = "Mesa sin nombre"
		}
		CreateNotification(db, order.TenantID,
			"Mesa llamando al mesero",
			label+" necesita atención",
			"waiter_call",
		)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"called_at": now,
			},
		})
	}
}
