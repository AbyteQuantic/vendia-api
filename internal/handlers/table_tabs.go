package handlers

import (
	"errors"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

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

		// Consolidate items by product_uuid to prevent duplicate rows.
		// The client should already send consolidated items, but rapid
		// syncs or buggy clients may send duplicates.
		merged := map[string]*ItemRequest{}
		var mergeOrder []string
		for i := range req.Items {
			it := &req.Items[i]
			if existing, ok := merged[it.ProductUUID]; ok {
				existing.Quantity += it.Quantity
			} else {
				merged[it.ProductUUID] = it
				mergeOrder = append(mergeOrder, it.ProductUUID)
			}
		}

		// Recompute total server-side — never trust the client.
		var total float64
		newItems := make([]models.OrderItem, 0, len(mergeOrder))
		for _, uuid := range mergeOrder {
			it := merged[uuid]
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
			// FOR UPDATE lock prevents concurrent syncs from racing
			err := tx.Set("gorm:query_option", "FOR UPDATE").
				Preload("Items").
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
						services.LogInventoryMovement(tx, services.MovementParams{
							TenantID:      tenantID,
							BranchID:      middleware.UUIDPtr(branchID),
							ProductID:     it.ProductUUID,
							ProductName:   it.ProductName,
							MovementType:  models.MovementTableTab,
							Quantity:      -it.Quantity,
							ReferenceID:   &created.ID,
							ReferenceType: "order",
							UserID:        middleware.UUIDPtr(userID),
						})
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
					services.LogInventoryMovement(tx, services.MovementParams{
						TenantID:      tenantID,
						BranchID:      middleware.UUIDPtr(branchID),
						ProductID:     uuid,
						MovementType:  models.MovementTableTab,
						Quantity:      -diff,
						ReferenceID:   &existing.ID,
						ReferenceType: "order",
						UserID:        middleware.UUIDPtr(userID),
					})
					tx.Model(&models.Product{}).
						Where("id = ? AND tenant_id = ?", uuid, tenantID).
						UpdateColumn("stock", gorm.Expr("GREATEST(stock - ?, 0)", diff))
				} else if diff < 0 {
					services.LogInventoryMovement(tx, services.MovementParams{
						TenantID:      tenantID,
						BranchID:      middleware.UUIDPtr(branchID),
						ProductID:     uuid,
						MovementType:  models.MovementTableTab,
						Quantity:      -diff,
						ReferenceID:   &existing.ID,
						ReferenceType: "order",
						UserID:        middleware.UUIDPtr(userID),
					})
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

// AddItemsToTableTab serves POST /api/v1/tables/tab/add-items.
// Accumulate-only: appends items to an existing open tab without
// removing anything. If the product already exists in the tab, its
// quantity is incremented. Creates the tab if none exists.
func AddItemsToTableTab(db *gorm.DB) gin.HandlerFunc {
	type ItemRequest struct {
		ProductUUID string  `json:"product_uuid" binding:"required"`
		ProductName string  `json:"product_name" binding:"required"`
		Quantity    int     `json:"quantity"      binding:"required,min=1"`
		UnitPrice   float64 `json:"unit_price"    binding:"required,gt=0"`
	}
	type Request struct {
		Label        string        `json:"label"  binding:"required"`
		Items        []ItemRequest `json:"items"  binding:"required"`
		CustomerName string        `json:"customer_name"`
		EmployeeName string        `json:"employee_name"`
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
			c.JSON(http.StatusBadRequest, gin.H{"error": "label vacío"})
			return
		}

		var result models.OrderTicket
		txErr := db.Transaction(func(tx *gorm.DB) error {
			var existing models.OrderTicket
			err := tx.Set("gorm:query_option", "FOR UPDATE").
				Preload("Items").
				Where("tenant_id = ? AND label = ? AND status IN ?",
					tenantID, label, []models.OrderStatus{
						models.OrderStatusNuevo,
						models.OrderStatusPreparando,
						models.OrderStatusListo,
					}).
				Order("created_at DESC").
				First(&existing).Error

			if errors.Is(err, gorm.ErrRecordNotFound) {
				// No open tab — create one with these items
				var items []models.OrderItem
				var total float64
				for _, it := range req.Items {
					total += it.UnitPrice * float64(it.Quantity)
					items = append(items, models.OrderItem{
						ProductUUID: it.ProductUUID,
						ProductName: it.ProductName,
						Quantity:    it.Quantity,
						UnitPrice:   it.UnitPrice,
					})
				}
				created := models.OrderTicket{
					TenantID:     tenantID,
					CreatedBy:    middleware.UUIDPtr(userID),
					BranchID:     middleware.UUIDPtr(branchID),
					Label:        label,
					CustomerName: req.CustomerName,
					EmployeeName: req.EmployeeName,
					Status:       models.OrderStatusNuevo,
					Type:         models.OrderTypeMesa,
					Total:        total,
					Items:        items,
				}
				if err := tx.Create(&created).Error; err != nil {
					return err
				}
				for _, it := range items {
					if it.ProductUUID != "" && it.Quantity > 0 {
						services.LogInventoryMovement(tx, services.MovementParams{
							TenantID:      tenantID,
							BranchID:      middleware.UUIDPtr(branchID),
							ProductID:     it.ProductUUID,
							ProductName:   it.ProductName,
							MovementType:  models.MovementTableTab,
							Quantity:      -it.Quantity,
							ReferenceID:   &created.ID,
							ReferenceType: "order",
							UserID:        middleware.UUIDPtr(userID),
						})
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

			// Existing tab — accumulate items
			// Build map of existing quantities
			existingQty := map[string]int{}
			for _, oi := range existing.Items {
				existingQty[oi.ProductUUID] += oi.Quantity
			}

			var addedTotal float64
			for _, it := range req.Items {
				addedTotal += it.UnitPrice * float64(it.Quantity)

				// Check if product already in the tab
				found := false
				for idx, oi := range existing.Items {
					if oi.ProductUUID == it.ProductUUID {
						// Increment quantity on existing row
						newQty := oi.Quantity + it.Quantity
						tx.Model(&existing.Items[idx]).Update("quantity", newQty)
						found = true
						break
					}
				}
				if !found {
					// Add new item row
					newItem := models.OrderItem{
						OrderUUID:   existing.ID,
						ProductUUID: it.ProductUUID,
						ProductName: it.ProductName,
						Quantity:    it.Quantity,
						UnitPrice:   it.UnitPrice,
					}
					tx.Create(&newItem)
				}

				// Deduct stock for the added quantity
				if it.ProductUUID != "" && it.Quantity > 0 {
					services.LogInventoryMovement(tx, services.MovementParams{
						TenantID:      tenantID,
						BranchID:      middleware.UUIDPtr(branchID),
						ProductID:     it.ProductUUID,
						ProductName:   it.ProductName,
						MovementType:  models.MovementTableTab,
						Quantity:      -it.Quantity,
						ReferenceID:   &existing.ID,
						ReferenceType: "order",
						UserID:        middleware.UUIDPtr(userID),
					})
					tx.Model(&models.Product{}).
						Where("id = ? AND tenant_id = ?", it.ProductUUID, tenantID).
						UpdateColumn("stock", gorm.Expr("GREATEST(stock - ?, 0)", it.Quantity))
				}
			}

			// Update total
			tx.Model(&existing).Update("total", existing.Total+addedTotal)

			// Reload
			tx.Preload("Items").First(&existing, "id = ?", existing.ID)
			result = existing
			return nil
		})

		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo agregar items",
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

// RemoveItemFromTab removes or decrements a single item from an open tab.
// DELETE /api/v1/orders/:uuid/items/:item_id
// Restores stock for the removed quantity.
func RemoveItemFromTab(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		orderUUID := c.Param("uuid")
		itemID := c.Param("item_id")

		var order models.OrderTicket
		if err := db.Where("id = ? AND tenant_id = ? AND status IN ?",
			orderUUID, tenantID, []models.OrderStatus{
				models.OrderStatusNuevo,
				models.OrderStatusPreparando,
				models.OrderStatusListo,
			}).First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cuenta no encontrada o ya cerrada"})
			return
		}

		var item models.OrderItem
		if err := db.Where("id = ? AND order_uuid = ?", itemID, orderUUID).
			First(&item).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "item no encontrado"})
			return
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			// Restore stock
			if item.ProductUUID != "" && item.Quantity > 0 {
				services.LogInventoryMovement(tx, services.MovementParams{
					TenantID:      tenantID,
					ProductID:     item.ProductUUID,
					ProductName:   item.ProductName,
					MovementType:  models.MovementOrderCancel,
					Quantity:      item.Quantity,
					ReferenceID:   &order.ID,
					ReferenceType: "order",
					UserID:        middleware.UUIDPtr(userID),
					Notes:         "item eliminado de cuenta abierta",
				})
				tx.Model(&models.Product{}).
					Where("id = ? AND tenant_id = ?", item.ProductUUID, tenantID).
					UpdateColumn("stock", gorm.Expr("stock + ?", item.Quantity))
			}

			// Remove the item
			if err := tx.Unscoped().Delete(&item).Error; err != nil {
				return err
			}

			// Recalculate total from remaining items
			var newTotal float64
			tx.Model(&models.OrderItem{}).
				Select("COALESCE(SUM(unit_price * quantity), 0)").
				Where("order_uuid = ?", orderUUID).
				Scan(&newTotal)

			return tx.Model(&order).Update("total", newTotal).Error
		})

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar item"})
			return
		}

		// Reload and return updated order
		db.Preload("Items").First(&order, "id = ?", orderUUID)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"order_id":      order.ID,
				"session_token": order.SessionToken,
				"label":         order.Label,
				"status":        order.Status,
				"total":         order.Total,
				"items":         order.Items,
			},
		})
	}
}
