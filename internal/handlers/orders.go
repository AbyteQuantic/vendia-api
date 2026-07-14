package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errOrderAlreadyClosed — sentinel para distinguir el cierre concurrente
// (otra request ganó la carrera) de un error real de BD en CloseOrder.
var errOrderAlreadyClosed = errors.New("order already closed")

func CreateOrder(db *gorm.DB) gin.HandlerFunc {
	type ItemRequest struct {
		ProductUUID string  `json:"product_uuid" binding:"required"`
		ProductName string  `json:"product_name" binding:"required"`
		Quantity    int     `json:"quantity"      binding:"required,min=1"`
		UnitPrice   float64 `json:"unit_price"    binding:"required,gt=0"`
		Emoji       string  `json:"emoji"`
		// Spec 105 — indicación del cliente por ítem ("sin cebolla").
		Notes string `json:"notes"`
	}

	type Request struct {
		ID              string           `json:"id"`
		Label           string           `json:"label"          binding:"required"`
		CustomerName    string           `json:"customer_name"`
		EmployeeUUID    string           `json:"employee_uuid"`
		EmployeeName    string           `json:"employee_name"`
		Type            models.OrderType `json:"type"`
		DeliveryAddress string           `json:"delivery_address"`
		CustomerPhone   string           `json:"customer_phone"`
		Items           []ItemRequest    `json:"items"          binding:"required,min=1"`
		// Spec 105 — mostrador PREPAGO: el POS ya registró la venta y manda
		// su UUID; el ticket nace pagado y su estado terminal es 'entregado'.
		SaleUUID string `json:"sale_uuid"`
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

		// Spec 105 — snapshot SERVER-SIDE del tiempo de preparación: se lee
		// Product.DurationMin de la BD (nunca del cliente) y se congela en el
		// ítem (patrón anti-drift Spec 083). Fail-open: sin fila/valor → nil.
		durationByProduct := map[string]*int{}
		{
			ids := make([]string, 0, len(req.Items))
			for _, it := range req.Items {
				if models.IsValidUUID(it.ProductUUID) {
					ids = append(ids, it.ProductUUID)
				}
			}
			if len(ids) > 0 {
				var rows []models.Product
				if err := db.Select("id", "duration_min").
					Where("tenant_id = ? AND id IN ?", tenantID, ids).
					Find(&rows).Error; err == nil {
					for _, r := range rows {
						durationByProduct[r.ID] = r.DurationMin
					}
				}
			}
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
				Notes:       strings.TrimSpace(item.Notes),
				DurationMin: durationByProduct[item.ProductUUID],
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
		// Spec 105 — mostrador prepago: atar la venta ya registrada en el POS.
		if models.IsValidUUID(req.SaleUUID) {
			now := time.Now()
			order.PaidAt = &now
			order.SaleUUID = &req.SaleUUID
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

		// Spec 105 — 'entregado' es logística (listo→entregado→cobrado) y
		// listo→cobrado se CONSERVA para las cuentas de mesa existentes.
		validTransitions := map[models.OrderStatus][]models.OrderStatus{
			models.OrderStatusNuevo:      {models.OrderStatusPreparando, models.OrderStatusCancelado},
			models.OrderStatusPreparando: {models.OrderStatusListo, models.OrderStatusCancelado},
			models.OrderStatusListo:      {models.OrderStatusEntregado, models.OrderStatusCobrado, models.OrderStatusCancelado},
			models.OrderStatusEntregado:  {models.OrderStatusCobrado},
		}

		// Spec 105 — guard IDEMPOTENTE (patrón concurrencia Spec 083): mesero
		// y cajero pueden marcar 'entregado' a la vez; el segundo PATCH no es
		// un error, devuelve el ticket tal cual (el chef solo necesita que
		// desaparezca UNA vez).
		if req.Status == order.Status && req.Status == models.OrderStatusEntregado {
			c.JSON(http.StatusOK, gin.H{"data": order, "message": "pedido ya estaba entregado"})
			return
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

		previousStatus := order.Status

		updates := map[string]any{"status": req.Status}
		if req.PaymentMethod != "" {
			updates["payment_method"] = req.PaymentMethod
		}
		// Spec 105 — estampar el timestamp de la transición (semáforo del KDS
		// y reporte prometido-vs-real).
		switch req.Status {
		case models.OrderStatusPreparando:
			updates["preparando_at"] = time.Now()
		case models.OrderStatusListo:
			updates["listo_at"] = time.Now()
		case models.OrderStatusEntregado:
			updates["entregado_at"] = time.Now()
		}

		if err := db.Model(&order).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar pedido"})
			return
		}

		// Restaurar stock SOLO al cancelar una orden que YA había descontado
		// (estaba 'cobrado'). En el modelo unificado de mesa el stock se descuenta
		// únicamente al COBRAR (close); una cuenta abierta nunca tocó inventario,
		// así que restaurarla lo INFLARÍA (council BUG-CANCEL-INFLATE Spec 083).
		if req.Status == models.OrderStatusCancelado && previousStatus == models.OrderStatusCobrado {
			// Kardex movement + stock restore run inside one transaction:
			// a kardex write failure must roll back the stock restore
			// instead of inflating stock with no audit trail (Art. VII).
			if err := db.Transaction(func(tx *gorm.DB) error {
				for _, item := range order.Items {
					if item.ProductUUID == "" || item.Quantity <= 0 {
						continue
					}
					if err := services.LogInventoryMovement(tx, services.MovementParams{
						TenantID:      tenantID,
						BranchID:      order.BranchID,
						ProductID:     item.ProductUUID,
						ProductName:   item.ProductName,
						MovementType:  models.MovementOrderCancel,
						Quantity:      item.Quantity,
						ReferenceID:   &order.ID,
						ReferenceType: "order",
						UserID:        middleware.UUIDPtr(middleware.GetUserID(c)),
					}); err != nil {
						return err
					}
					if err := tx.Model(&models.Product{}).
						Where("id = ? AND tenant_id = ?", item.ProductUUID, tenantID).
						UpdateColumn("stock", gorm.Expr("stock + ?", item.Quantity)).Error; err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al restaurar inventario"})
				return
			}
		}

		// MERMA al cancelar un PLATO ya preparado (Spec 083, decisión fundador:
		// "cancelar una gaseosa ≠ cancelar una hamburguesa"). Si la cuenta ya
		// estaba en cocina ('preparando'/'listo') y el ítem es una receta, sus
		// insumos YA se consumieron → se registran como consumo (pérdida real), no
		// se restauran. Producto directo (gaseosa) = no-op en ExplodeRecipe (se
		// revende). Plato 'nuevo' (no cocinado) → no entra acá.
		if req.Status == models.OrderStatusCancelado &&
			(previousStatus == models.OrderStatusPreparando || previousStatus == models.OrderStatusListo) {
			rs := services.NewRecipeService(db)
			for _, item := range order.Items {
				if item.ProductUUID == "" || item.Quantity <= 0 {
					continue
				}
				// Ancla de idempotencia distinta de una venta real: re-cancelar
				// no vuelve a descontar insumos.
				_ = rs.ExplodeRecipe(db, services.ExplodeParams{
					TenantID:  tenantID,
					SaleUUID:  "cancel:" + order.ID,
					ProductID: item.ProductUUID,
					Quantity:  item.Quantity,
					BranchID:  order.BranchID,
					UserID:    middleware.UUIDPtr(middleware.GetUserID(c)),
				})
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
			OrderID string  `gorm:"column:order_id"`
			Paid    float64 `gorm:"column:paid"`
		}
		var sums []PaidSum
		if len(orderIDs) > 0 {
			db.Model(&models.PartialPayment{}).
				Select("order_id, COALESCE(SUM(amount), 0) AS paid").
				Where("order_id IN ? AND status = 'APPROVED'", orderIDs).
				Group("order_id").
				Scan(&sums)
		}
		paidMap := map[string]float64{}
		for _, s := range sums {
			paidMap[s.OrderID] = s.Paid
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
		// Spec 105 — mostrador PREPAGO: la venta YA se registró en el cobro
		// POS (paid_at/sale_uuid). Cerrar aquí crearía una SEGUNDA venta y
		// descontaría stock dos veces. Estado terminal correcto: 'entregado'.
		if order.PaidAt != nil {
			c.JSON(http.StatusConflict, gin.H{
				"error": "este pedido ya fue pagado en caja; márquelo como entregado",
				"code":  "already_paid",
			})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// saleInventory applies the same inventory side effects a POS
		// sale does — FR-02. Built once; ApplyPostSale receives the
		// active transaction so the discount is atomic with the sale row.
		saleInventory := services.NewSaleInventoryService(db)

		err := db.Transaction(func(tx *gorm.DB) error {
			// Anti doble-cierre CONCURRENTE (council BUG-CLOSE-RACE Spec 083): el
			// check de estado de arriba está FUERA de la tx. Aquí bloqueamos la
			// fila y hacemos un UPDATE CONDICIONADO por estado; si 0 filas se
			// afectaron, otra request ya cobró → abortamos sin crear 2ª venta ni
			// descontar inventario dos veces.
			var locked models.OrderTicket
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND tenant_id = ?", uuid, tenantID).
				First(&locked).Error; err != nil {
				return err
			}
			if locked.Status == models.OrderStatusCobrado || locked.Status == models.OrderStatusCancelado {
				return errOrderAlreadyClosed
			}
			res := tx.Model(&models.OrderTicket{}).
				Where("id = ? AND tenant_id = ? AND status NOT IN ?", uuid, tenantID,
					[]models.OrderStatus{models.OrderStatusCobrado, models.OrderStatusCancelado}).
				Updates(map[string]any{
					"status":         models.OrderStatusCobrado,
					"payment_method": req.PaymentMethod,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errOrderAlreadyClosed
			}

			var saleItems []models.SaleItem
			var inventoryLines []services.SaleInventoryLine
			// Spec 084 (backlog #5) — congela la comisión de los servicios al
			// cerrar la comanda (mismo modelo que la venta directa). El resolver
			// cachea la config de pago por profesional (una query por persona);
			// es la MISMA resolución que usa CreateSale (servicio compartido).
			commissions := services.NewCommissionResolver(tx, tenantID)
			for _, item := range order.Items {
				pid := item.ProductUUID
				si := models.SaleItem{
					ProductID:    &pid,
					Name:         item.ProductName,
					Price:        item.UnitPrice,
					Quantity:     item.Quantity,
					Subtotal:     item.UnitPrice * float64(item.Quantity),
					EmployeeUUID: item.EmployeeUUID,
					EmployeeName: item.EmployeeName,
				}
				// Si el producto es un servicio y la línea tiene profesional,
				// congela la comisión (base = subtotal; salones suelen ser exentos).
				if item.EmployeeUUID != nil && *item.EmployeeUUID != "" && item.ProductUUID != "" {
					var p models.Product
					if err := tx.Select("is_service", "commission_pct").
						Where("id = ? AND tenant_id = ?", item.ProductUUID, tenantID).
						First(&p).Error; err == nil && p.IsService {
						si.IsService = true
						basis, pct, amount := services.ResolveLineCommission(
							commissions.Config(*item.EmployeeUUID), p.CommissionPct, si.Subtotal)
						si.PayBasis = basis
						si.CommissionPct = pct
						si.CommissionAmount = amount
					}
				}
				saleItems = append(saleItems, si)
				// Queue the line for the shared inventory service. A
				// blank product UUID (legacy / ad-hoc item) is skipped —
				// it has no stock to move.
				if item.ProductUUID != "" {
					inventoryLines = append(inventoryLines, services.SaleInventoryLine{
						ProductID: item.ProductUUID,
						Quantity:  item.Quantity,
					})
				}
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
			if err := tx.Create(&sale).Error; err != nil {
				return err
			}

			// FR-02 — closing a KDS order must discount inventory and
			// explode recipes exactly like CreateSale. The sale UUID
			// anchors the recipe-explosion idempotency so a retried
			// close never double-discounts insumos (Art. II). A failure
			// aborts the transaction, keeping the order, the sale and
			// the inventory consistent.
			return saleInventory.ApplyPostSale(tx, services.PostSaleParams{
				TenantID: tenantID,
				SaleUUID: sale.ID,
				BranchID: order.BranchID,
				UserID:   order.EmployeeUUID,
				Lines:    inventoryLines,
			})
		})

		if errors.Is(err, errOrderAlreadyClosed) {
			c.JSON(http.StatusConflict, gin.H{"error": "el pedido ya está cerrado"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al cerrar pedido"})
			return
		}

		order.Status = models.OrderStatusCobrado
		c.JSON(http.StatusOK, gin.H{"data": order})
	}
}
