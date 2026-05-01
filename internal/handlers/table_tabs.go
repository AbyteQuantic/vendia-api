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
// Upserts a table tab with stock management:
//   - CREATE: deducts stock for all items
//   - UPDATE: computes diff (old vs new) and adjusts stock accordingly
//     (deduct for increases, restore for decreases/removals)
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

		var result models.OrderTicket
		txErr := db.Transaction(func(tx *gorm.DB) error {
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
				// ── CREATE new ticket ──
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
				// Deduct stock for all new items
				for _, it := range newItems {
					if it.ProductUUID != "" && it.Quantity > 0 {
						tx.Model(&models.Product{}).
							Where("id = ? AND tenant_id = ?", it.ProductUUID, tenantID).
							UpdateColumn("stock", gorm.Expr("GREATEST(stock - ?, 0)", it.Quantity))
					}
				}
				result = created
				return nil
			}
			if err != nil {
				return err
			}

			// ── UPDATE existing ticket with stock diff ──

			// 1. Old quantities by product UUID
			oldQty := map[string]int{}
			for _, oi := range existing.Items {
				oldQty[oi.ProductUUID] += oi.Quantity
			}

			// 2. Replace items atomically
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

			// 3. New quantities by product UUID
			newQty := map[string]int{}
			for _, ni := range newItems {
				newQty[ni.ProductUUID] += ni.Quantity
			}

			// 4. Apply stock diff
			allUUIDs := map[string]bool{}
			for k := range oldQty {
				allUUIDs[k] = true
			}
			for k := range newQty {
				allUUIDs[k] = true
			}
			for uuid := range allUUIDs {
				if uuid == "" {
					continue
				}
				diff := newQty[uuid] - oldQty[uuid]
				if diff > 0 {
					// More items ordered → deduct stock
					tx.Model(&models.Product{}).
						Where("id = ? AND tenant_id = ?", uuid, tenantID).
						UpdateColumn("stock", gorm.Expr("GREATEST(stock - ?, 0)", diff))
				} else if diff < 0 {
					// Items removed → restore stock
					tx.Model(&models.Product{}).
						Where("id = ? AND tenant_id = ?", uuid, tenantID).
						UpdateColumn("stock", gorm.Expr("stock + ?", -diff))
				}
			}

			// 5. Update total + metadata
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
