package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func CreateOrder(db *gorm.DB) gin.HandlerFunc {
	type ItemRequest struct {
		ProductUUID string  `json:"product_uuid" binding:"required"`
		ProductName string  `json:"product_name" binding:"required"`
		Quantity    int     `json:"quantity"      binding:"required,min=1"`
		UnitPrice   float64 `json:"unit_price"    binding:"required,gt=0"`
		Emoji       string  `json:"emoji"`
	}

	type Request struct {
		ID              string              `json:"id"`
		Label           string              `json:"label"          binding:"required"`
		CustomerName    string              `json:"customer_name"`
		EmployeeUUID    string              `json:"employee_uuid"`
		EmployeeName    string              `json:"employee_name"`
		Type            models.OrderType    `json:"type"`
		DeliveryAddress string              `json:"delivery_address"`
		CustomerPhone   string              `json:"customer_phone"`
		Items           []ItemRequest       `json:"items"          binding:"required,min=1"`
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

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID v4 válido"})
			return
		}

		if req.Type == "" {
			req.Type = models.OrderTypeMesa
		}

		var total float64
		var items []models.OrderItem
		for _, item := range req.Items {
			subtotal := item.UnitPrice * float64(item.Quantity)
			total += subtotal
			items = append(items, models.OrderItem{
				ProductUUID: item.ProductUUID,
				ProductName: item.ProductName,
				Quantity:    item.Quantity,
				UnitPrice:   item.UnitPrice,
				Emoji:       item.Emoji,
			})
		}

		order := models.OrderTicket{
			TenantID:        tenantID,
			CreatedBy:       middleware.UUIDPtr(userID),
			BranchID:        middleware.UUIDPtr(branchID),
			Label:           req.Label,
			CustomerName:    req.CustomerName,
			EmployeeUUID:    middleware.UUIDPtr(req.EmployeeUUID),
			EmployeeName:    req.EmployeeName,
			Status:          models.OrderStatusNuevo,
			Type:            req.Type,
			Total:           total,
			DeliveryAddress: req.DeliveryAddress,
			CustomerPhone:   req.CustomerPhone,
			Items:           items,
		}
		if req.ID != "" {
			order.ID = req.ID
		}

		if err := db.Create(&order).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear pedido"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": order})
	}
}

func ListOrders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		status := c.Query("status")

		query := ApplyBranchScope(db.Where("tenant_id = ?", tenantID), scope)
		if status != "" {
			query = query.Where("status = ?", status)
		}

		var orders []models.OrderTicket
		if err := query.Preload("Items").
			Order("created_at DESC").
			Find(&orders).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener pedidos"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": orders})
	}
}

func GetOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var order models.OrderTicket
		if err := db.Preload("Items").
			Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": order})
	}
}

func UpdateOrderStatus(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Status        models.OrderStatus `json:"status"         binding:"required"`
		PaymentMethod string             `json:"payment_method"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var order models.OrderTicket
		if err := db.Preload("Items").Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		validTransitions := map[models.OrderStatus][]models.OrderStatus{
			models.OrderStatusNuevo:      {models.OrderStatusPreparando, models.OrderStatusCancelado},
			models.OrderStatusPreparando: {models.OrderStatusListo, models.OrderStatusCancelado},
			models.OrderStatusListo:      {models.OrderStatusCobrado, models.OrderStatusCancelado},
		}

		allowed, ok := validTransitions[order.Status]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el pedido no se puede modificar"})
			return
		}

		valid := false
		for _, s := range allowed {
			if s == req.Status {
				valid = true
				break
			}
		}
		if !valid {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "transición de estado no permitida",
			})
			return
		}

		updates := map[string]any{"status": req.Status}
		if req.PaymentMethod != "" {
			updates["payment_method"] = req.PaymentMethod
		}

		if err := db.Model(&order).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar pedido"})
			return
		}

		// Restore stock when cancelling — return all items to inventory
		if req.Status == models.OrderStatusCancelado {
			for _, item := range order.Items {
				if item.ProductUUID != "" && item.Quantity > 0 {
					services.LogInventoryMovement(db, services.MovementParams{
						TenantID:      tenantID,
						ProductID:     item.ProductUUID,
						ProductName:   item.ProductName,
						MovementType:  models.MovementOrderCancel,
						Quantity:      item.Quantity,
						ReferenceID:   &order.ID,
						ReferenceType: "order",
						UserID:        middleware.UUIDPtr(middleware.GetUserID(c)),
					})
					db.Model(&models.Product{}).
						Where("id = ? AND tenant_id = ?", item.ProductUUID, tenantID).
						UpdateColumn("stock", gorm.Expr("stock + ?", item.Quantity))
				}
			}
		}

		order.Status = req.Status
		c.JSON(http.StatusOK, gin.H{"data": order})
	}
}

func OpenAccounts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		var orders []models.OrderTicket
		q := ApplyBranchScope(db.Preload("Items"), scope).
			Where("tenant_id = ? AND status IN (?, ?, ?)", tenantID,
				models.OrderStatusNuevo, models.OrderStatusPreparando, models.OrderStatusListo).
			Order("created_at ASC")
		if err := q.Find(&orders).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener cuentas abiertas"})
			return
		}

		// Enrich with paid_amount so the POS banner shows the real
		// outstanding balance (total - abonos) instead of the gross total.
		orderIDs := make([]string, len(orders))
		for i, o := range orders {
			orderIDs[i] = o.ID
		}
		type PaidSum struct {
			OrderTicketID string  `gorm:"column:order_ticket_id"`
			Paid          float64 `gorm:"column:paid"`
		}
		var sums []PaidSum
		if len(orderIDs) > 0 {
			db.Model(&models.PartialPayment{}).
				Select("order_ticket_id, COALESCE(SUM(amount), 0) AS paid").
				Where("order_ticket_id IN ? AND status = 'APPROVED'", orderIDs).
				Group("order_ticket_id").
				Scan(&sums)
		}
		paidMap := map[string]float64{}
		for _, s := range sums {
			paidMap[s.OrderTicketID] = s.Paid
		}

		type OrderWithBalance struct {
			models.OrderTicket
			PaidAmount     float64 `json:"paid_amount"`
			PendingBalance float64 `json:"pending_balance"`
		}
		result := make([]OrderWithBalance, len(orders))
		for i, o := range orders {
			paid := paidMap[o.ID]
			result[i] = OrderWithBalance{
				OrderTicket:    o,
				PaidAmount:     paid,
				PendingBalance: o.Total - paid,
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}

func CloseOrder(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PaymentMethod string `json:"payment_method" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var order models.OrderTicket
		if err := db.Preload("Items").
			Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}

		if order.Status == models.OrderStatusCobrado || order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el pedido ya está cerrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&order).Updates(map[string]any{
				"status":         models.OrderStatusCobrado,
				"payment_method": req.PaymentMethod,
			}).Error; err != nil {
				return err
			}

			var saleItems []models.SaleItem
			for _, item := range order.Items {
				pid := item.ProductUUID
				saleItems = append(saleItems, models.SaleItem{
					ProductID: &pid,
					Name:      item.ProductName,
					Price:     item.UnitPrice,
					Quantity:  item.Quantity,
					Subtotal:  item.UnitPrice * float64(item.Quantity),
				})
			}

			sale := models.Sale{
				TenantID:      tenantID,
				BranchID:      order.BranchID,
				Total:         order.Total,
				PaymentMethod: models.PaymentMethod(req.PaymentMethod),
				EmployeeUUID:  order.EmployeeUUID,
				EmployeeName:  order.EmployeeName,
				// Distinguish table-closed sales from POS quick-sales
				// in the unified ledger so the dashboard can split
				// totals by channel without re-joining order_tickets.
				Source: models.SaleSourceTable,
				Items:  saleItems,
			}
			return tx.Create(&sale).Error
		})

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al cerrar pedido"})
			return
		}

		order.Status = models.OrderStatusCobrado
		c.JSON(http.StatusOK, gin.H{"data": order})
	}
}
