// Spec: specs/003-trabajos-muebles/spec.md
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
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// workOrderResponse wraps a WorkOrder with the computed abonado / saldo
// figures the Plan §4 contract requires. It embeds the model so every
// persisted field (including the JSON `id`) is serialised unchanged,
// and adds the two derived fields on top — the model itself is never
// mutated (Art. IX).
type workOrderResponse struct {
	models.WorkOrder
	Abonado float64 `json:"abonado"`
	Saldo   float64 `json:"saldo"`
}

// newWorkOrderResponse builds the response envelope for one work order.
func newWorkOrderResponse(wo models.WorkOrder) workOrderResponse {
	return workOrderResponse{
		WorkOrder: wo,
		Abonado:   wo.Paid(),
		Saldo:     wo.Balance(),
	}
}

// woItemInput is one item line of a create / update work-order request.
// A `material` line references an insumo XOR a product; a `mano_obra`
// line references no inventory (Spec §7 invariant).
type woItemInput struct {
	Kind         string  `json:"kind"`
	IngredientID *string `json:"ingredient_id"`
	ProductID    *string `json:"product_id"`
	Description  string  `json:"description"`
	Quantity     float64 `json:"quantity"`
	UnitPrice    float64 `json:"unit_price"`
}

// buildWorkOrderItems validates the request lines and turns them into
// WorkOrderItem rows. Returns a Spanish error on the first invalid line
// (FR-02, §9).
func buildWorkOrderItems(woID string, inputs []woItemInput) ([]models.WorkOrderItem, error) {
	if len(inputs) == 0 {
		return nil, errors.New("el trabajo necesita al menos un ítem")
	}
	items := make([]models.WorkOrderItem, 0, len(inputs))
	for _, in := range inputs {
		if !models.IsValidWorkOrderItemKind(in.Kind) {
			return nil, errors.New("cada ítem debe ser de tipo material o mano_obra")
		}
		item := models.WorkOrderItem{
			WorkOrderID:  woID,
			Kind:         in.Kind,
			IngredientID: middleware.UUIDPtr(deref(in.IngredientID)),
			ProductID:    middleware.UUIDPtr(deref(in.ProductID)),
			Description:  strings.TrimSpace(in.Description),
			Quantity:     in.Quantity,
			UnitPrice:    in.UnitPrice,
		}
		if !item.IsValidReference() {
			if in.Kind == models.WorkOrderItemMaterial {
				return nil, errors.New("un ítem de material debe referenciar un insumo o un producto (no ambos)")
			}
			return nil, errors.New("un ítem de mano de obra no puede referenciar inventario")
		}
		if !item.HasValidAmounts() {
			return nil, errors.New("la cantidad y el precio de cada ítem deben ser mayores a cero")
		}
		items = append(items, item)
	}
	return items, nil
}

// loadWorkOrder fetches a tenant-scoped work order with its items and
// payments, or nil when it does not exist.
func loadWorkOrder(db *gorm.DB, tenantID, woID string) (*models.WorkOrder, error) {
	var wo models.WorkOrder
	if err := db.Preload("Items").Preload("Payments").
		Where("id = ? AND tenant_id = ?", woID, tenantID).
		First(&wo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &wo, nil
}

// ListWorkOrders returns the tenant's work orders, paginated, optionally
// filtered by ?status= / ?type= (Plan §4).
func ListWorkOrders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		query := db.Model(&models.WorkOrder{}).Where("tenant_id = ?", tenantID)
		if status := c.Query("status"); status != "" {
			if !models.IsValidWorkOrderStatus(status) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "estado inválido"})
				return
			}
			query = query.Where("status = ?", status)
		}
		if woType := c.Query("type"); woType != "" {
			if !models.IsValidWorkOrderType(woType) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "tipo inválido"})
				return
			}
			query = query.Where("type = ?", woType)
		}

		var total int64
		query.Count(&total)

		var orders []models.WorkOrder
		if err := query.
			Preload("Items").
			Preload("Payments").
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&orders).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener los trabajos"})
			return
		}

		data := make([]workOrderResponse, 0, len(orders))
		for _, wo := range orders {
			data = append(data, newWorkOrderResponse(wo))
		}
		c.JSON(http.StatusOK, newPaginatedResponse(data, total, p))
	}
}

// GetWorkOrder returns a single tenant-scoped work order with its items,
// payments and the computed abonado / saldo.
func GetWorkOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		wo, err := loadWorkOrder(db, tenantID, c.Param("uuid"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener el trabajo"})
			return
		}
		if wo == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "trabajo no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": newWorkOrderResponse(*wo)})
	}
}

// CreateWorkOrder registers a new work order in `cotizacion` with its
// items (FR-01). Idempotent by client-supplied UUID (Art. II).
func CreateWorkOrder(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID          string        `json:"id"`
		CustomerID  string        `json:"customer_id"`
		Type        string        `json:"type"`
		Description string        `json:"description"`
		Notes       string        `json:"notes"`
		Items       []woItemInput `json:"items"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID v4 válido"})
			return
		}
		if strings.TrimSpace(req.CustomerID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el cliente es obligatorio"})
			return
		}
		if !models.IsValidWorkOrderType(req.Type) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el tipo debe ser fabricacion o reparacion"})
			return
		}

		// Idempotency (Art. II): a re-sent client UUID returns the
		// existing row instead of failing on the primary key.
		if req.ID != "" {
			existing, err := loadWorkOrder(db, tenantID, req.ID)
			if err == nil && existing != nil {
				c.JSON(http.StatusCreated, gin.H{"data": newWorkOrderResponse(*existing)})
				return
			}
		}

		// The customer must exist for this tenant (Art. III, VI).
		var customerCount int64
		db.Model(&models.Customer{}).
			Where("id = ? AND tenant_id = ?", req.CustomerID, tenantID).
			Count(&customerCount)
		if customerCount == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cliente no encontrado"})
			return
		}

		woID := req.ID
		if woID == "" {
			woID = uuid.NewString()
		}

		items, err := buildWorkOrderItems(woID, req.Items)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		wo := models.WorkOrder{
			BaseModel:   models.BaseModel{ID: woID},
			TenantID:    tenantID,
			CustomerID:  req.CustomerID,
			Type:        req.Type,
			Status:      models.WorkOrderQuote,
			Description: strings.TrimSpace(req.Description),
			Notes:       strings.TrimSpace(req.Notes),
			Items:       items,
		}
		wo.Total = wo.ComputeTotal()

		if err := db.Create(&wo).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear el trabajo"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": newWorkOrderResponse(wo)})
	}
}

// UpdateWorkOrder applies a partial update: notes/description always
// editable; items editable only while `cotizacion`/`aprobada` (FR-07,
// AC-07); status transitions validated against the lifecycle machine
// (FR-03, AC-05). A transition to `terminada` delegates to the
// WorkOrderService so material stock is discounted via the kardex.
func UpdateWorkOrder(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Description *string        `json:"description"`
		Notes       *string        `json:"notes"`
		Status      *string        `json:"status"`
		Items       *[]woItemInput `json:"items"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		woID := c.Param("uuid")

		wo, err := loadWorkOrder(db, tenantID, woID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener el trabajo"})
			return
		}
		if wo == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "trabajo no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// A status change is validated against the lifecycle machine.
		if req.Status != nil {
			next := *req.Status
			if !models.IsValidWorkOrderStatus(next) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "estado inválido"})
				return
			}
			if next != wo.Status && !wo.CanTransitionTo(next) {
				c.JSON(http.StatusConflict, gin.H{
					"error": "transición de estado no permitida",
				})
				return
			}
			// §9 — a work order with no items cannot be approved.
			if next == models.WorkOrderApproved && len(wo.Items) == 0 {
				c.JSON(http.StatusUnprocessableEntity, gin.H{
					"error": "un trabajo sin ítems no se puede aprobar",
				})
				return
			}
		}

		// Items can only be replaced while the order is editable.
		if req.Items != nil && !wo.ItemsEditable() {
			c.JSON(http.StatusConflict, gin.H{
				"error": "solo se pueden editar los ítems de un trabajo en cotización o aprobada",
			})
			return
		}

		// A transition to `terminada` is the kardex-consuming path: it
		// runs through the WorkOrderService so material stock is
		// discounted idempotently. Item edits are not allowed on the
		// same request (the order is no longer editable at en_proceso).
		completing := req.Status != nil && *req.Status == models.WorkOrderCompleted

		err = db.Transaction(func(tx *gorm.DB) error {
			updates := map[string]any{}
			if req.Description != nil {
				updates["description"] = strings.TrimSpace(*req.Description)
			}
			if req.Notes != nil {
				updates["notes"] = strings.TrimSpace(*req.Notes)
			}
			if req.Status != nil && !completing {
				updates["status"] = *req.Status
				if *req.Status == models.WorkOrderApproved {
					updates["approved_at"] = time.Now()
				}
				if *req.Status == models.WorkOrderDelivered {
					updates["delivered_at"] = time.Now()
				}
			}

			if req.Items != nil {
				items, berr := buildWorkOrderItems(wo.ID, *req.Items)
				if berr != nil {
					return berr
				}
				// Replace the item set: hard-delete the old rows so a
				// re-edit never accumulates stale lines, then recreate.
				if derr := tx.Unscoped().
					Where("work_order_id = ?", wo.ID).
					Delete(&models.WorkOrderItem{}).Error; derr != nil {
					return derr
				}
				var total float64
				for i := range items {
					if cerr := tx.Create(&items[i]).Error; cerr != nil {
						return cerr
					}
					total += items[i].LineTotal()
				}
				updates["total"] = total
			}

			if len(updates) > 0 {
				return tx.Model(&models.WorkOrder{}).
					Where("id = ? AND tenant_id = ?", wo.ID, tenantID).
					Updates(updates).Error
			}
			return nil
		})
		if err != nil {
			// A validation error from buildWorkOrderItems is a 400.
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Run the completion (material consumption) after the metadata
		// update so the kardex sees the persisted items.
		if completing {
			svc := services.NewWorkOrderService(db)
			if _, cerr := svc.CompleteWorkOrder(tenantID, wo.ID, services.WorkOrderContext{
				BranchID: middleware.GetBranchIDPtr(c),
				UserID:   middleware.GetUserIDPtr(c),
			}); cerr != nil {
				c.JSON(completeErrorStatus(cerr), gin.H{"error": cerr.Error()})
				return
			}
		}

		fresh, _ := loadWorkOrder(db, tenantID, wo.ID)
		c.JSON(http.StatusOK, gin.H{"data": newWorkOrderResponse(*fresh)})
	}
}

// completeErrorStatus maps a WorkOrderService completion error to its
// HTTP code.
func completeErrorStatus(err error) int {
	switch {
	case errors.Is(err, services.ErrWONotFound):
		return http.StatusNotFound
	case errors.Is(err, services.ErrWONotCompletable):
		return http.StatusConflict
	case errors.Is(err, services.ErrWOItemInvalid):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// DeleteWorkOrder soft-deletes the work order.
func DeleteWorkOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		result := db.Where("id = ? AND tenant_id = ?", c.Param("uuid"), tenantID).
			Delete(&models.WorkOrder{})
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar el trabajo"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "trabajo no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "trabajo eliminado"})
	}
}

// CreateWorkOrderPayment registers a customer advance against a work
// order (FR-04, AC-02). An advance cannot exceed the outstanding
// balance (§7).
func CreateWorkOrderPayment(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID     string  `json:"id"`
		Amount float64 `json:"amount"`
		Method string  `json:"method"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		woID := c.Param("uuid")

		wo, err := loadWorkOrder(db, tenantID, woID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener el trabajo"})
			return
		}
		if wo == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "trabajo no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID v4 válido"})
			return
		}
		if req.Amount <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el anticipo debe ser mayor a cero"})
			return
		}

		// Idempotency (Art. II): a re-sent client UUID is a no-op.
		if req.ID != "" {
			var existing int64
			db.Model(&models.WorkOrderPayment{}).Where("id = ?", req.ID).Count(&existing)
			if existing > 0 {
				c.JSON(http.StatusCreated, gin.H{"data": newWorkOrderResponse(*wo)})
				return
			}
		}

		// §7 — an advance cannot exceed the outstanding balance.
		if req.Amount > wo.Balance() {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "el anticipo no puede superar el saldo pendiente",
			})
			return
		}

		payment := models.WorkOrderPayment{
			BaseModel:   models.BaseModel{ID: req.ID},
			TenantID:    tenantID,
			WorkOrderID: wo.ID,
			Amount:      req.Amount,
			Method:      strings.TrimSpace(req.Method),
			PaidAt:      time.Now(),
		}
		if err := db.Create(&payment).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al registrar el anticipo"})
			return
		}

		fresh, _ := loadWorkOrder(db, tenantID, wo.ID)
		c.JSON(http.StatusCreated, gin.H{"data": newWorkOrderResponse(*fresh)})
	}
}

// ShareWorkOrder builds a wa.me URL with the quotation breakdown and the
// total, for the customer (FR-06, AC-06). Only a work order in
// `cotizacion` can be shared.
func ShareWorkOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		woID := c.Param("uuid")

		wo, err := loadWorkOrder(db, tenantID, woID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener el trabajo"})
			return
		}
		if wo == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "trabajo no encontrado"})
			return
		}
		if wo.Status != models.WorkOrderQuote {
			c.JSON(http.StatusConflict, gin.H{
				"error": "solo se puede compartir un trabajo en cotización",
			})
			return
		}

		var customer models.Customer
		if err := db.Where("id = ? AND tenant_id = ?", wo.CustomerID, tenantID).
			First(&customer).Error; err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "cliente no encontrado"})
			return
		}

		var tenant models.Tenant
		businessName := "su negocio"
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err == nil &&
			strings.TrimSpace(tenant.BusinessName) != "" {
			businessName = tenant.BusinessName
		}

		lines := make([]services.WorkOrderQuoteLine, 0, len(wo.Items))
		for _, item := range wo.Items {
			lines = append(lines, services.WorkOrderQuoteLine{
				Description: item.Description,
				Quantity:    item.Quantity,
				LineTotal:   item.LineTotal(),
			})
		}

		waSvc := services.NewWhatsAppService()
		message := waSvc.WorkOrderQuote(customer.Name, businessName, lines, wo.Total)
		waURL := waSvc.BuildURL(customer.Phone, message)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"whatsapp_url": waURL,
				"message":      message,
			},
		})
	}
}
