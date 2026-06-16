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
// Upsert idempotente del estado COMPLETO de una mesa (por label). El tab es un
// BORRADOR: persiste/recalcula ítems y total pero NO toca stock (Spec 052 — el
// stock se descuenta una sola vez al cobrar, vía CloseOrder). Es el endpoint que
// el cliente offline-first usa para EMPUJAR el estado de la mesa (Spec 053).
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
				// Spec 052: el tab es un BORRADOR — NO descuenta stock al agregar.
				// El stock se descuenta una sola vez al cobrar (CloseOrder →
				// ApplyPostSale). Antes se descontaba acá Y al cerrar = doble
				// descuento (fuga de stock confirmada en prod).
				result = created
				return nil
			}
			if err != nil {
				return err
			}

			// ── UPDATE existing ticket — reemplaza ítems, SIN tocar stock ──
			// Spec 052: el tab es un borrador; el stock se mueve solo al cobrar.

			// Reemplaza los ítems atómicamente.
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

			// Update total + metadata
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
				// Spec 053: updated_at habilita LWW por mesa en el sync offline.
				"updated_at": result.UpdatedAt,
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
				// Spec 052: el tab no descuenta stock al agregar (solo al cobrar).
				result = created
				return nil
			}
			if err != nil {
				return err
			}

			// Existing tab — accumulate items (Spec 052: sin tocar stock).
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
				// Spec 052: sin descuento de stock al agregar (solo al cobrar).
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
				// Spec 053: updated_at habilita LWW por mesa en el sync offline.
				"updated_at": result.UpdatedAt,
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
				"updated_at":    ticket.UpdatedAt,
			},
		})
	}
}

// ListOpenTableTabs serves GET /api/v1/tables/open — lista TODAS las mesas
// abiertas del tenant (OrderTickets nuevo/preparando/listo) con ítems y
// updated_at. Lo usa el cliente offline-first para "traer" las mesas al
// arrancar/reconectar (un dispositivo nuevo no conoce los labels, así que
// GET /tables/tab/:label no alcanza). Spec 053.
func ListOpenTableTabs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var tickets []models.OrderTicket
		if err := db.Preload("Items").
			Where("tenant_id = ? AND status IN ?", tenantID, []models.OrderStatus{
				models.OrderStatusNuevo,
				models.OrderStatusPreparando,
				models.OrderStatusListo,
			}).
			Order("updated_at DESC").
			Find(&tickets).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener mesas abiertas"})
			return
		}
		out := make([]gin.H, 0, len(tickets))
		for _, t := range tickets {
			out = append(out, gin.H{
				"order_id":      t.ID,
				"session_token": t.SessionToken,
				"label":         t.Label,
				"status":        t.Status,
				"total":         t.Total,
				"type":          t.Type,
				"items":         t.Items,
				"opened_at":     t.CreatedAt,
				"updated_at":    t.UpdatedAt,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// RemoveItemFromTab removes or decrements a single item from an open tab.
// DELETE /api/v1/orders/:uuid/items/:item_id
// Spec 052: el tab no descontó stock al agregar, así que quitar NO lo restaura.
func RemoveItemFromTab(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
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
			// Spec 052: el tab no descontó stock al agregar, así que quitar un
			// ítem NO restaura stock (no había nada que revertir). El stock solo
			// se mueve al cobrar (CloseOrder).

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
