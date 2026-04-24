package handlers

import (
	"errors"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UpsertTableTab serves PUT /api/v1/tables/tab.
//
// Why this endpoint exists separately from POST /orders:
//
//   POST /orders creates a NEW ticket every time. That works for
//   "para_llevar" and one-shot mostrador sales, but for a table
//   tab the cashier accumulates items across multiple POS rounds
//   and needs a STABLE session_token for the live-tab QR to stay
//   valid between rounds. A fresh ticket per round would:
//     - issue a new session_token each time (broken QR posters),
//     - scatter the same "Mesa 1" across many rows in the KDS,
//     - prevent "Cobrar y enviar" from rolling one total.
//
// So this endpoint does a tenant-scoped upsert keyed by table
// label: if an open (nuevo / preparando / listo) ticket exists
// for (tenant, label), we replace its items and recompute the
// total; otherwise we create a new one. In both cases we return
// the SAME session_token the ticket has owned since its first
// creation — that's the whole point.
func UpsertTableTab(db *gorm.DB) gin.HandlerFunc {
	type ItemRequest struct {
		ProductUUID string  `json:"product_uuid" binding:"required"`
		ProductName string  `json:"product_name" binding:"required"`
		Quantity    int     `json:"quantity"      binding:"required,min=1"`
		UnitPrice   float64 `json:"unit_price"    binding:"required,gt=0"`
		Emoji       string  `json:"emoji"`
	}
	type Request struct {
		Label        string           `json:"label"         binding:"required"`
		Type         models.OrderType `json:"type"`
		Items        []ItemRequest    `json:"items"         binding:"required"`
		CustomerName string           `json:"customer_name"`
		EmployeeUUID string           `json:"employee_uuid"`
		EmployeeName string           `json:"employee_name"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		branchID := middleware.GetBranchID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		label := strings.TrimSpace(req.Label)
		if label == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "label no puede estar vacío"})
			return
		}
		if req.Type == "" {
			req.Type = models.OrderTypeMesa
		}

		// Recompute total server-side — never trust the client.
		var total float64
		newItems := make([]models.OrderItem, 0, len(req.Items))
		for _, it := range req.Items {
			total += it.UnitPrice * float64(it.Quantity)
			newItems = append(newItems, models.OrderItem{
				ProductUUID: it.ProductUUID,
				ProductName: it.ProductName,
				Quantity:    it.Quantity,
				UnitPrice:   it.UnitPrice,
				Emoji:       it.Emoji,
			})
		}

		// Single DB transaction so the replace-items-then-update-total
		// sequence can't land in a torn state mid-flight.
		var result models.OrderTicket
		txErr := db.Transaction(func(tx *gorm.DB) error {
			// Look for an OPEN ticket for this exact label. Status
			// whitelist matches OpenAccounts() so the two endpoints
			// agree on what "open" means.
			var existing models.OrderTicket
			openStatuses := []models.OrderStatus{
				models.OrderStatusNuevo,
				models.OrderStatusPreparando,
				models.OrderStatusListo,
			}
			err := tx.Preload("Items").
				Where("tenant_id = ? AND label = ? AND status IN ?",
					tenantID, label, openStatuses).
				Order("created_at DESC").
				First(&existing).Error

			if errors.Is(err, gorm.ErrRecordNotFound) {
				// First save for this table → CREATE. BeforeCreate
				// takes care of the UUID + session_token in one go.
				created := models.OrderTicket{
					TenantID:     tenantID,
					CreatedBy:    middleware.UUIDPtr(userID),
					BranchID:     middleware.UUIDPtr(branchID),
					Label:        label,
					CustomerName: req.CustomerName,
					EmployeeUUID: middleware.UUIDPtr(req.EmployeeUUID),
					EmployeeName: req.EmployeeName,
					Status:       models.OrderStatusNuevo,
					Type:         req.Type,
					Total:        total,
					Items:        newItems,
				}
				if err := tx.Create(&created).Error; err != nil {
					return err
				}
				result = created
				return nil
			}
			if err != nil {
				return err
			}

			// Ticket exists → REPLACE items atomically. We delete the
			// rows instead of doing a diff-merge because:
			//   a) the client is the source of truth (it holds the
			//      whole local cart),
			//   b) OrderItem has no per-line state we care about
			//      beyond price / qty, which we're about to re-send.
			// session_token stays the same because we never touch
			// the parent row's ID.
			if err := tx.Where("order_uuid = ?", existing.ID).
				Delete(&models.OrderItem{}).Error; err != nil {
				return err
			}
			for i := range newItems {
				newItems[i].OrderUUID = existing.ID
			}
			if len(newItems) > 0 {
				if err := tx.Create(&newItems).Error; err != nil {
					return err
				}
			}
			// Update total + any metadata the client refreshed.
			updates := map[string]any{"total": total}
			if req.CustomerName != "" {
				updates["customer_name"] = req.CustomerName
			}
			if req.EmployeeName != "" {
				updates["employee_name"] = req.EmployeeName
			}
			if err := tx.Model(&existing).Updates(updates).Error; err != nil {
				return err
			}
			// Reload so the caller receives the fresh items.
			if err := tx.Preload("Items").First(&existing, "id = ?", existing.ID).Error; err != nil {
				return err
			}
			result = existing
			return nil
		})

		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo guardar la cuenta",
				"detail": txErr.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"order_id":      result.ID,
				"session_token": result.SessionToken,
				"label":         result.Label,
				"status":        result.Status,
				"total":         result.Total,
				"type":          result.Type,
				"items":         result.Items,
			},
		})
	}
}

// GetTableTab serves GET /api/v1/tables/tab/:label.
//
// Authenticated sibling of the public live-tab endpoint. The
// cashier uses this to look up the session_token on demand —
// e.g. when the QR sheet opens but the local POS hasn't yet
// seen the ticket (reinstalled app, different device).
//
// Returns 404 when there is no open ticket for the label.
func GetTableTab(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		label := strings.TrimSpace(c.Param("label"))
		if label == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "label vacío"})
			return
		}

		var ticket models.OrderTicket
		err := db.Preload("Items").
			Where("tenant_id = ? AND label = ? AND status IN ?",
				tenantID, label, []models.OrderStatus{
					models.OrderStatusNuevo,
					models.OrderStatusPreparando,
					models.OrderStatusListo,
				}).
			Order("created_at DESC").
			First(&ticket).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sin cuenta abierta para esa mesa"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al cargar la cuenta",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"order_id":      ticket.ID,
				"session_token": ticket.SessionToken,
				"label":         ticket.Label,
				"status":        ticket.Status,
				"total":         ticket.Total,
				"type":          ticket.Type,
				"items":         ticket.Items,
			},
		})
	}
}
