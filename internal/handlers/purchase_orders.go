// Spec: specs/002-ordenes-compra/spec.md
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

// poItemInput is one item line of a create / update purchase-order
// request. An item references an insumo XOR a product (D1).
type poItemInput struct {
	IngredientID *string `json:"ingredient_id"`
	ProductID    *string `json:"product_id"`
	Quantity     float64 `json:"quantity"`
	UnitCost     float64 `json:"unit_cost"`
}

// buildItems validates the request lines and turns them into
// PurchaseOrderItem rows, resolving the name snapshot from the
// referenced insumo/producto. Returns a Spanish error on the first
// invalid line (FR-02, §9).
func buildItems(db *gorm.DB, tenantID, poID string, inputs []poItemInput) ([]models.PurchaseOrderItem, error) {
	if len(inputs) == 0 {
		return nil, errors.New("la orden de compra necesita al menos un ítem")
	}
	items := make([]models.PurchaseOrderItem, 0, len(inputs))
	for _, in := range inputs {
		item := models.PurchaseOrderItem{
			PurchaseOrderID: poID,
			IngredientID:    middleware.UUIDPtr(deref(in.IngredientID)),
			ProductID:       middleware.UUIDPtr(deref(in.ProductID)),
			Quantity:        in.Quantity,
			UnitCost:        in.UnitCost,
		}
		if !item.IsValidReference() {
			return nil, errors.New("cada ítem debe referenciar un insumo o un producto (no ambos)")
		}
		if !item.HasValidAmounts() {
			return nil, errors.New("la cantidad y el costo de cada ítem deben ser mayores a cero")
		}
		item.NameSnapshot = resolveItemName(db, tenantID, item)
		items = append(items, item)
	}
	return items, nil
}

// resolveItemName looks up the current name of the referenced
// insumo/producto for the snapshot. A miss falls back to a generic
// label so the PO is never blocked on a name lookup (Art. I).
func resolveItemName(db *gorm.DB, tenantID string, item models.PurchaseOrderItem) string {
	if item.IngredientID != nil {
		var ing models.Ingredient
		if err := db.Select("name").
			Where("id = ? AND tenant_id = ?", *item.IngredientID, tenantID).
			First(&ing).Error; err == nil {
			return ing.Name
		}
		return "Insumo"
	}
	if item.ProductID != nil {
		var prod models.Product
		if err := db.Select("name").
			Where("id = ? AND tenant_id = ?", *item.ProductID, tenantID).
			First(&prod).Error; err == nil {
			return prod.Name
		}
		return "Producto"
	}
	return ""
}

// loadPurchaseOrder fetches a tenant-scoped PO with its items, or nil
// when it does not exist.
func loadPurchaseOrder(db *gorm.DB, tenantID, poID string) (*models.PurchaseOrder, error) {
	var po models.PurchaseOrder
	if err := db.Preload("Items").
		Where("id = ? AND tenant_id = ?", poID, tenantID).
		First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &po, nil
}

// ListPurchaseOrders returns the tenant's purchase orders, paginated,
// optionally filtered by ?status= (Plan §4).
func ListPurchaseOrders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		query := db.Model(&models.PurchaseOrder{}).Where("tenant_id = ?", tenantID)
		if status := c.Query("status"); status != "" {
			if !models.IsValidPurchaseOrderStatus(status) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "estado inválido"})
				return
			}
			query = query.Where("status = ?", status)
		}

		var total int64
		query.Count(&total)

		var orders []models.PurchaseOrder
		if err := query.
			Preload("Items").
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&orders).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener órdenes de compra"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(orders, total, p))
	}
}

// GetPurchaseOrder returns a single tenant-scoped PO with its items.
func GetPurchaseOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		po, err := loadPurchaseOrder(db, tenantID, c.Param("uuid"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener la orden de compra"})
			return
		}
		if po == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "orden de compra no encontrada"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": po})
	}
}

// CreatePurchaseOrder registers a new PO in `borrador` with its items
// (FR-01). Idempotent by client-supplied UUID (Art. II).
func CreatePurchaseOrder(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID         string        `json:"id"`
		SupplierID string        `json:"supplier_id"`
		Notes      string        `json:"notes"`
		Items      []poItemInput `json:"items"`
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
		if strings.TrimSpace(req.SupplierID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el proveedor es obligatorio"})
			return
		}

		// Idempotency (Art. II): a re-sent client UUID returns the
		// existing row instead of failing on the primary key.
		if req.ID != "" {
			existing, err := loadPurchaseOrder(db, tenantID, req.ID)
			if err == nil && existing != nil {
				c.JSON(http.StatusCreated, gin.H{"data": existing})
				return
			}
		}

		// The supplier must exist for this tenant (Art. III, VI).
		var supplierCount int64
		db.Model(&models.Supplier{}).
			Where("id = ? AND tenant_id = ?", req.SupplierID, tenantID).
			Count(&supplierCount)
		if supplierCount == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "proveedor no encontrado"})
			return
		}

		poID := req.ID
		if poID == "" {
			poID = uuid.NewString()
		}

		items, err := buildItems(db, tenantID, poID, req.Items)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		po := models.PurchaseOrder{
			BaseModel:  models.BaseModel{ID: poID},
			TenantID:   tenantID,
			SupplierID: req.SupplierID,
			Status:     models.PurchaseOrderDraft,
			Notes:      strings.TrimSpace(req.Notes),
			Items:      items,
		}
		po.Total = po.ComputeTotal()

		if err := db.Create(&po).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear la orden de compra"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": po})
	}
}

// UpdatePurchaseOrder applies a partial update: notes always editable;
// items editable only while `borrador`; status transitions validated
// against the lifecycle machine (FR-03).
func UpdatePurchaseOrder(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Notes  *string        `json:"notes"`
		Status *string        `json:"status"`
		Items  *[]poItemInput `json:"items"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		poID := c.Param("uuid")

		po, err := loadPurchaseOrder(db, tenantID, poID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener la orden de compra"})
			return
		}
		if po == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "orden de compra no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// A status change is validated against the lifecycle machine.
		// Receiving via PATCH is NOT allowed — it must go through the
		// /receive endpoint so stock enters via the kardex (D4).
		if req.Status != nil {
			next := *req.Status
			if !models.IsValidPurchaseOrderStatus(next) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "estado inválido"})
				return
			}
			if next == models.PurchaseOrderReceived {
				c.JSON(http.StatusConflict, gin.H{
					"error": "para recibir una orden use el endpoint de recepción",
				})
				return
			}
			if next != po.Status && !po.CanTransitionTo(next) {
				c.JSON(http.StatusConflict, gin.H{
					"error": "transición de estado no permitida",
				})
				return
			}
		}

		// Items can only be replaced while the PO is still a draft.
		if req.Items != nil && po.Status != models.PurchaseOrderDraft {
			c.JSON(http.StatusConflict, gin.H{
				"error": "solo se pueden editar los ítems de una orden en borrador",
			})
			return
		}

		err = db.Transaction(func(tx *gorm.DB) error {
			updates := map[string]any{}
			if req.Notes != nil {
				updates["notes"] = strings.TrimSpace(*req.Notes)
			}
			if req.Status != nil {
				updates["status"] = *req.Status
			}

			if req.Items != nil {
				items, berr := buildItems(tx, tenantID, po.ID, *req.Items)
				if berr != nil {
					return berr
				}
				// Replace the item set: hard-delete the old rows so a
				// re-edit never accumulates stale lines, then recreate.
				if derr := tx.Unscoped().
					Where("purchase_order_id = ?", po.ID).
					Delete(&models.PurchaseOrderItem{}).Error; derr != nil {
					return derr
				}
				for i := range items {
					if cerr := tx.Create(&items[i]).Error; cerr != nil {
						return cerr
					}
				}
				var total float64
				for _, it := range items {
					total += it.LineTotal()
				}
				updates["total"] = total
			}

			if len(updates) > 0 {
				return tx.Model(&models.PurchaseOrder{}).
					Where("id = ? AND tenant_id = ?", po.ID, tenantID).
					Updates(updates).Error
			}
			return nil
		})
		if err != nil {
			// A validation error from buildItems is a 400.
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		fresh, _ := loadPurchaseOrder(db, tenantID, po.ID)
		c.JSON(http.StatusOK, gin.H{"data": fresh})
	}
}

// DeletePurchaseOrder soft-deletes the PO.
func DeletePurchaseOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		result := db.Where("id = ? AND tenant_id = ?", c.Param("uuid"), tenantID).
			Delete(&models.PurchaseOrder{})
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar la orden de compra"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "orden de compra no encontrada"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "orden de compra eliminada"})
	}
}

// SendPurchaseOrder flips a draft PO to `enviada` and returns a wa.me
// URL with the complete item list (FR-04, AC-02).
func SendPurchaseOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		poID := c.Param("uuid")

		po, err := loadPurchaseOrder(db, tenantID, poID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener la orden de compra"})
			return
		}
		if po == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "orden de compra no encontrada"})
			return
		}
		if !po.CanTransitionTo(models.PurchaseOrderSent) {
			c.JSON(http.StatusConflict, gin.H{
				"error": "la orden ya fue enviada o no puede enviarse en su estado actual",
			})
			return
		}
		if len(po.Items) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "la orden no tiene ítems para enviar",
			})
			return
		}

		var supplier models.Supplier
		if err := db.Where("id = ? AND tenant_id = ?", po.SupplierID, tenantID).
			First(&supplier).Error; err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "proveedor no encontrado"})
			return
		}

		var tenant models.Tenant
		ownerName := ""
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err == nil {
			ownerName = tenant.OwnerName
		}

		lines := make([]services.PurchaseOrderLine, 0, len(po.Items))
		for _, item := range po.Items {
			lines = append(lines, services.PurchaseOrderLine{
				Name:     item.NameSnapshot,
				Quantity: item.Quantity,
				Unit:     resolveItemUnit(db, tenantID, item),
			})
		}

		waSvc := services.NewWhatsAppService()
		message := waSvc.PurchaseOrder(supplier.ContactName, ownerName, lines)
		waURL := waSvc.BuildURL(supplier.Phone, message)

		now := time.Now()
		if err := db.Model(&models.PurchaseOrder{}).
			Where("id = ? AND tenant_id = ?", po.ID, tenantID).
			Updates(map[string]any{
				"status":  models.PurchaseOrderSent,
				"sent_at": now,
			}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al enviar la orden de compra"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"status":       models.PurchaseOrderSent,
				"whatsapp_url": waURL,
				"message":      message,
			},
		})
	}
}

// resolveItemUnit looks up the measurement unit of a line's insumo so
// the WhatsApp message reads "10 kg de Arroz". A product line has no
// unit enum, so it falls back to "unidad".
func resolveItemUnit(db *gorm.DB, tenantID string, item models.PurchaseOrderItem) string {
	if item.IngredientID != nil {
		var ing models.Ingredient
		if err := db.Select("unit").
			Where("id = ? AND tenant_id = ?", *item.IngredientID, tenantID).
			First(&ing).Error; err == nil && ing.Unit != "" {
			return ing.Unit
		}
	}
	return models.UnitUnidad
}

// ReceivePurchaseOrderHandler receives a PO: enters stock for every
// item via the kardex and flips the PO to `recibida` (FR-05, AC-03/04).
func ReceivePurchaseOrderHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		poID := c.Param("uuid")

		svc := services.NewPurchaseService(db)
		po, err := svc.ReceivePurchaseOrder(tenantID, poID, services.ReceiveContext{
			BranchID: middleware.GetBranchIDPtr(c),
			UserID:   middleware.GetUserIDPtr(c),
		})
		if err != nil {
			c.JSON(receiveErrorStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": po})
	}
}

// receiveErrorStatus maps a PurchaseService error to its HTTP code.
func receiveErrorStatus(err error) int {
	switch {
	case errors.Is(err, services.ErrPONotFound):
		return http.StatusNotFound
	case errors.Is(err, services.ErrPONotReceivable):
		return http.StatusConflict
	case errors.Is(err, services.ErrPOEmpty):
		return http.StatusUnprocessableEntity
	case errors.Is(err, services.ErrPOItemInvalid):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// PurchaseOrdersFromReorder generates a draft PO per supplier from the
// low-stock insumos and products of the tenant (FR-07, AC-07). Items
// without a supplier are skipped — there is no proveedor to address.
func PurchaseOrdersFromReorder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		// reorderLine is a low-stock item awaiting a PO, already
		// reduced to the PurchaseOrderItem shape.
		type reorderLine struct {
			ingredientID *string
			productID    *string
			name         string
			quantity     float64
			unitCost     float64
		}

		grouped := map[string][]reorderLine{}

		// Low-stock insumos.
		var ingredients []models.Ingredient
		if err := db.Where("tenant_id = ? AND min_stock > 0 AND stock < min_stock", tenantID).
			Find(&ingredients).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener insumos bajos"})
			return
		}
		for _, ing := range ingredients {
			if ing.SupplierID == nil || *ing.SupplierID == "" {
				continue
			}
			qty := ing.MinStock - ing.Stock
			if qty < 1 {
				qty = 1
			}
			id := ing.ID
			grouped[*ing.SupplierID] = append(grouped[*ing.SupplierID], reorderLine{
				ingredientID: &id, name: ing.Name, quantity: qty, unitCost: ing.UnitCost,
			})
		}

		// Low-stock products.
		var products []models.Product
		if err := db.Where("tenant_id = ? AND is_available = true AND min_stock > 0 AND stock < min_stock", tenantID).
			Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener productos bajos"})
			return
		}
		for _, prod := range products {
			if prod.SupplierID == nil || *prod.SupplierID == "" {
				continue
			}
			qty := float64(prod.MinStock - prod.Stock)
			if qty < 1 {
				qty = 1
			}
			id := prod.ID
			grouped[*prod.SupplierID] = append(grouped[*prod.SupplierID], reorderLine{
				productID: &id, name: prod.Name, quantity: qty, unitCost: prod.PurchasePrice,
			})
		}

		created := make([]models.PurchaseOrder, 0, len(grouped))
		err := db.Transaction(func(tx *gorm.DB) error {
			for supplierID, lines := range grouped {
				// Skip a supplier that no longer exists for the tenant.
				var supplierCount int64
				tx.Model(&models.Supplier{}).
					Where("id = ? AND tenant_id = ?", supplierID, tenantID).
					Count(&supplierCount)
				if supplierCount == 0 {
					continue
				}

				poID := uuid.NewString()
				items := make([]models.PurchaseOrderItem, 0, len(lines))
				for _, l := range lines {
					items = append(items, models.PurchaseOrderItem{
						PurchaseOrderID: poID,
						IngredientID:    l.ingredientID,
						ProductID:       l.productID,
						NameSnapshot:    l.name,
						Quantity:        l.quantity,
						UnitCost:        l.unitCost,
					})
				}
				po := models.PurchaseOrder{
					BaseModel:  models.BaseModel{ID: poID},
					TenantID:   tenantID,
					SupplierID: supplierID,
					Status:     models.PurchaseOrderDraft,
					Items:      items,
				}
				po.Total = po.ComputeTotal()
				if cerr := tx.Create(&po).Error; cerr != nil {
					return cerr
				}
				created = append(created, po)
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar las órdenes de compra"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": created})
	}
}
