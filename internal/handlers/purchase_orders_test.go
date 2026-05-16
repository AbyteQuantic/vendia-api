// Spec: specs/002-ordenes-compra/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupPurchaseOrdersDB opens an in-memory sqlite DB with every table
// the purchase-order handlers touch. The Tenant model's
// `default:gen_random_uuid()` breaks AutoMigrate on sqlite, so the
// tenants table is hand-crafted (same trick as branches_test).
func setupPurchaseOrdersDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT NOT NULL DEFAULT '',
			owner_name TEXT NOT NULL DEFAULT '',
			phone TEXT DEFAULT '', created_at DATETIME
		);
	`).Error)
	require.NoError(t, db.AutoMigrate(
		&models.PurchaseOrder{},
		&models.PurchaseOrderItem{},
		&models.Ingredient{},
		&models.Product{},
		&models.Supplier{},
		&models.InventoryMovement{},
	))
	return db
}

func mountPurchaseOrders(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.GET("/purchase-orders", handlers.ListPurchaseOrders(db))
	r.POST("/purchase-orders", handlers.CreatePurchaseOrder(db))
	r.GET("/purchase-orders/:uuid", handlers.GetPurchaseOrder(db))
	r.PATCH("/purchase-orders/:uuid", handlers.UpdatePurchaseOrder(db))
	r.DELETE("/purchase-orders/:uuid", handlers.DeletePurchaseOrder(db))
	r.POST("/purchase-orders/:uuid/send", handlers.SendPurchaseOrder(db))
	r.POST("/purchase-orders/:uuid/receive", handlers.ReceivePurchaseOrderHandler(db))
	r.POST("/purchase-orders/from-reorder", handlers.PurchaseOrdersFromReorder(db))
	return r
}

// ptrStr returns a pointer to s — a local helper for seeding optional
// *string FK fields in the purchase-order tests.
func ptrStr(s string) *string { return &s }

func seedPOSupplier(t *testing.T, db *gorm.DB, tenantID, supplierID, name string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, owner_name, created_at)
		VALUES (?, 'Tienda Test', 'Don Pepe', ?) ON CONFLICT(id) DO NOTHING`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Create(&models.Supplier{
		BaseModel: models.BaseModel{ID: supplierID},
		TenantID:  tenantID, CompanyName: name,
		ContactName: "Pedro", Phone: "3001234567",
	}).Error)
}

// poResponse mirrors the {data: PurchaseOrder} envelope.
type poResponse struct {
	Data models.PurchaseOrder `json:"data"`
}

// AC-01 — create a PO with 2 items, then read it back: supplier, items
// with quantity/cost and the computed total are all visible.
func TestCreatePurchaseOrder_PersistsWithItemsAndTotal(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "Distribuidora ABC")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 3}
	require.NoError(t, db.Create(&ing).Error)
	prod := models.Product{TenantID: "tenant-a", Name: "Gaseosa", Price: 2500, Stock: 5}
	require.NoError(t, db.Create(&prod).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders", map[string]any{
		"supplier_id": "sup-1",
		"notes":       "Pedido semanal",
		"items": []map[string]any{
			{"ingredient_id": ing.ID, "quantity": 10, "unit_cost": 2900},
			{"product_id": prod.ID, "quantity": 12, "unit_cost": 2000},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp poResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "sup-1", resp.Data.SupplierID)
	assert.Equal(t, models.PurchaseOrderDraft, resp.Data.Status)
	assert.Len(t, resp.Data.Items, 2)
	// 10*2900 + 12*2000 = 53000
	assert.Equal(t, float64(53000), resp.Data.Total)

	// Read it back via GET.
	w2 := doJSON(t, r, http.MethodGet, "/purchase-orders/"+resp.Data.ID, nil)
	require.Equal(t, http.StatusOK, w2.Code)
	var got poResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &got))
	assert.Len(t, got.Data.Items, 2)
	assert.Equal(t, float64(53000), got.Data.Total)
	// The model exposes the BaseModel ID as JSON "id" (not "uuid").
	assert.NotEmpty(t, got.Data.ID)
}

// FR-02 — an item must reference an insumo XOR a product.
func TestCreatePurchaseOrder_RejectsItemWithBothOrNeitherRef(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	r := mountPurchaseOrders(db, "tenant-a")

	// Both refs set.
	wBoth := doJSON(t, r, http.MethodPost, "/purchase-orders", map[string]any{
		"supplier_id": "sup-1",
		"items": []map[string]any{
			{"ingredient_id": "i1", "product_id": "p1", "quantity": 1, "unit_cost": 1},
		},
	})
	assert.Equal(t, http.StatusBadRequest, wBoth.Code, wBoth.Body.String())

	// Neither ref set.
	wNone := doJSON(t, r, http.MethodPost, "/purchase-orders", map[string]any{
		"supplier_id": "sup-1",
		"items": []map[string]any{
			{"quantity": 1, "unit_cost": 1},
		},
	})
	assert.Equal(t, http.StatusBadRequest, wNone.Code, wNone.Body.String())
}

// §9 — quantity / cost ≤ 0 is rejected.
func TestCreatePurchaseOrder_RejectsNonPositiveAmounts(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	r := mountPurchaseOrders(db, "tenant-a")

	for _, bad := range []map[string]any{
		{"ingredient_id": "i1", "quantity": 0, "unit_cost": 100},
		{"ingredient_id": "i1", "quantity": 5, "unit_cost": 0},
		{"ingredient_id": "i1", "quantity": -1, "unit_cost": 100},
	} {
		w := doJSON(t, r, http.MethodPost, "/purchase-orders", map[string]any{
			"supplier_id": "sup-1",
			"items":       []map[string]any{bad},
		})
		assert.Equal(t, http.StatusBadRequest, w.Code, "item %v must 400", bad)
	}
}

// VI — supplier_id is required and must exist for the tenant.
func TestCreatePurchaseOrder_RejectsMissingOrForeignSupplier(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	seedPOSupplier(t, db, "tenant-b", "sup-b", "Otro")
	r := mountPurchaseOrders(db, "tenant-a")

	// Missing supplier.
	wMissing := doJSON(t, r, http.MethodPost, "/purchase-orders", map[string]any{
		"items": []map[string]any{{"ingredient_id": "i1", "quantity": 1, "unit_cost": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, wMissing.Code)

	// Supplier belongs to another tenant.
	wForeign := doJSON(t, r, http.MethodPost, "/purchase-orders", map[string]any{
		"supplier_id": "sup-b",
		"items":       []map[string]any{{"ingredient_id": "i1", "quantity": 1, "unit_cost": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, wForeign.Code)
}

// Idempotent create — re-sending the same client UUID does not duplicate.
func TestCreatePurchaseOrder_IdempotentByUUID(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	r := mountPurchaseOrders(db, "tenant-a")
	id := "11111111-1111-4111-8111-111111111111"

	for i := 0; i < 2; i++ {
		w := doJSON(t, r, http.MethodPost, "/purchase-orders", map[string]any{
			"id":          id,
			"supplier_id": "sup-1",
			"items":       []map[string]any{{"ingredient_id": "i1", "quantity": 1, "unit_cost": 100}},
		})
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.PurchaseOrder{}).Where("id = ?", id).Count(&count)
	assert.Equal(t, int64(1), count, "re-sending the same UUID must not duplicate")
}

// AC-02 — sending a draft PO flips it to enviada and returns a wa.me URL
// with the complete item list.
func TestSendPurchaseOrder_TransitionsAndReturnsWhatsAppURL(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "po222222-2222-4222-8222-222222222222"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderDraft,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: ptrStr("i1"), NameSnapshot: "Arroz", Quantity: 10, UnitCost: 2900},
			{PurchaseOrderID: poID, IngredientID: ptrStr("i2"), NameSnapshot: "Aceite", Quantity: 5, UnitCost: 8000},
		},
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/send", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Status      string `json:"status"`
			WhatsAppURL string `json:"whatsapp_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.PurchaseOrderSent, resp.Data.Status)
	assert.Contains(t, resp.Data.WhatsAppURL, "wa.me/")
	assert.Contains(t, resp.Data.WhatsAppURL, "Arroz")
	assert.Contains(t, resp.Data.WhatsAppURL, "Aceite")

	var po models.PurchaseOrder
	require.NoError(t, db.First(&po, "id = ?", poID).Error)
	assert.Equal(t, models.PurchaseOrderSent, po.Status)
	require.NotNil(t, po.SentAt, "sent_at must be stamped")
}

// §9 — a PO with no items cannot be sent.
func TestSendPurchaseOrder_RejectsEmptyPO(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "po333333-3333-4333-8333-333333333333"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderDraft,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/send", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

// FR-03 — a PO already enviada cannot be sent again.
func TestSendPurchaseOrder_RejectsAlreadySent(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "po444444-4444-4444-8444-444444444444"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: ptrStr("i1"), NameSnapshot: "X", Quantity: 1, UnitCost: 1},
		},
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/send", nil)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

// AC-06 — cancelling is done via PATCH status; a sent PO can be
// cancelled, then receiving a cancelled PO is rejected.
func TestUpdatePurchaseOrder_CancelThenReceiveRejected(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 3}
	require.NoError(t, db.Create(&ing).Error)
	poID := "po555555-5555-4555-8555-555555555555"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: &ing.ID, NameSnapshot: "Arroz", Quantity: 10, UnitCost: 2900},
		},
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")

	// Cancel via PATCH.
	wCancel := doJSON(t, r, http.MethodPatch, "/purchase-orders/"+poID, map[string]any{
		"status": models.PurchaseOrderCancelled,
	})
	require.Equal(t, http.StatusOK, wCancel.Code, wCancel.Body.String())

	var po models.PurchaseOrder
	require.NoError(t, db.First(&po, "id = ?", poID).Error)
	assert.Equal(t, models.PurchaseOrderCancelled, po.Status)

	// Receiving a cancelled PO is rejected and stock untouched.
	wReceive := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/receive", nil)
	assert.Equal(t, http.StatusConflict, wReceive.Code, wReceive.Body.String())

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", ing.ID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "a cancelled PO must not move stock")
}

// FR-03 — an invalid PATCH transition (received → sent) is rejected.
func TestUpdatePurchaseOrder_RejectsInvalidTransition(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "po666666-6666-4666-8666-666666666666"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderReceived,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/purchase-orders/"+poID, map[string]any{
		"status": models.PurchaseOrderSent,
	})
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

// FR-03 — editing items is allowed only while the PO is borrador.
func TestUpdatePurchaseOrder_RejectsItemEditAfterDraft(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "po777777-7777-4777-8777-777777777777"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: ptrStr("i1"), NameSnapshot: "X", Quantity: 1, UnitCost: 1},
		},
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/purchase-orders/"+poID, map[string]any{
		"items": []map[string]any{{"ingredient_id": "i9", "quantity": 2, "unit_cost": 99}},
	})
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

// FR-01 — editing items of a draft PO replaces them and recomputes total.
func TestUpdatePurchaseOrder_ReplacesItemsAndRecomputesTotal(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "po888888-8888-4888-8888-888888888888"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderDraft,
		Total: 1000,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: ptrStr("i1"), NameSnapshot: "X", Quantity: 1, UnitCost: 1000},
		},
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/purchase-orders/"+poID, map[string]any{
		"items": []map[string]any{
			{"ingredient_id": "i2", "quantity": 4, "unit_cost": 500},
		},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var po models.PurchaseOrder
	require.NoError(t, db.Preload("Items").First(&po, "id = ?", poID).Error)
	assert.Len(t, po.Items, 1)
	assert.Equal(t, float64(2000), po.Total, "total recomputed: 4*500")
}

// DELETE soft-deletes the PO.
func TestDeletePurchaseOrder_SoftDeletes(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "po999999-9999-4999-8999-999999999999"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderDraft,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodDelete, "/purchase-orders/"+poID, nil)
	require.Equal(t, http.StatusOK, w.Code)

	var count int64
	db.Model(&models.PurchaseOrder{}).Where("id = ?", poID).Count(&count)
	assert.Equal(t, int64(0), count, "soft-deleted PO must not surface")
}

// Art. III — list is scoped to the tenant; another tenant's POs never leak.
func TestListPurchaseOrders_TenantIsolationAndStatusFilter(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	require.NoError(t, db.Create(&models.PurchaseOrder{
		TenantID: "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderDraft,
	}).Error)
	require.NoError(t, db.Create(&models.PurchaseOrder{
		TenantID: "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
	}).Error)
	require.NoError(t, db.Create(&models.PurchaseOrder{
		TenantID: "tenant-b", SupplierID: "sup-x", Status: models.PurchaseOrderDraft,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")

	// All of tenant-a.
	wAll := doJSON(t, r, http.MethodGet, "/purchase-orders", nil)
	require.Equal(t, http.StatusOK, wAll.Code)
	var respAll handlers.PaginatedResponse
	require.NoError(t, json.Unmarshal(wAll.Body.Bytes(), &respAll))
	assert.Equal(t, int64(2), respAll.Total, "only tenant-a's POs")

	// Filtered by status.
	wDraft := doJSON(t, r, http.MethodGet, "/purchase-orders?status=enviada", nil)
	require.Equal(t, http.StatusOK, wDraft.Code)
	var respDraft handlers.PaginatedResponse
	require.NoError(t, json.Unmarshal(wDraft.Body.Bytes(), &respDraft))
	assert.Equal(t, int64(1), respDraft.Total, "only the enviada PO")
}

// AC-03 — receiving a sent PO via the HTTP endpoint enters stock and
// returns the received PO.
func TestReceivePurchaseOrderHandler_EntersStock(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 3, UnitCost: 2900}
	require.NoError(t, db.Create(&ing).Error)
	poID := "poaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: &ing.ID, NameSnapshot: "Arroz", Quantity: 10, UnitCost: 2900},
		},
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/receive", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp poResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.PurchaseOrderReceived, resp.Data.Status)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", ing.ID).Error)
	assert.InDelta(t, 13.0, arroz.Stock, 1e-9, "arroz 3 + 10 = 13 (AC-03)")
}

// AC-04 — receiving the same PO twice via HTTP does not double stock.
func TestReceivePurchaseOrderHandler_Idempotent(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 3}
	require.NoError(t, db.Create(&ing).Error)
	poID := "pobbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: &ing.ID, NameSnapshot: "Arroz", Quantity: 10, UnitCost: 2900},
		},
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w1 := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/receive", nil)
	require.Equal(t, http.StatusOK, w1.Code)
	w2 := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/receive", nil)
	require.Equal(t, http.StatusOK, w2.Code, "re-receiving must not error")

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", ing.ID).Error)
	assert.InDelta(t, 13.0, arroz.Stock, 1e-9, "stock must NOT double (AC-04)")
}

// AC-07 — generating POs from reorder produces a draft PO per supplier
// with all the low-stock items of that supplier as lines.
func TestPurchaseOrdersFromReorder_GroupsBySupplier(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "Distribuidora ABC")

	// A low-stock insumo of sup-1.
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Arroz", Unit: "kg",
		Stock: 1, MinStock: 5, UnitCost: 2900, SupplierID: ptrStr("sup-1"),
	}).Error)
	// A low-stock product of sup-1.
	require.NoError(t, db.Create(&models.Product{
		TenantID: "tenant-a", Name: "Gaseosa", Price: 2500,
		PurchasePrice: 2000, Stock: 2, MinStock: 10, SupplierID: ptrStr("sup-1"),
	}).Error)
	// A healthy insumo — must NOT be ordered.
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Sal", Unit: "g",
		Stock: 100, MinStock: 10, SupplierID: ptrStr("sup-1"),
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/from-reorder", nil)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data []models.PurchaseOrder `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1, "one PO for the single supplier with low stock")
	po := resp.Data[0]
	assert.Equal(t, "sup-1", po.SupplierID)
	assert.Equal(t, models.PurchaseOrderDraft, po.Status)
	require.Len(t, po.Items, 2, "the low-stock insumo + product, not the healthy one")

	// The PO was persisted as a draft.
	var count int64
	db.Model(&models.PurchaseOrder{}).Where("tenant_id = ? AND status = ?",
		"tenant-a", models.PurchaseOrderDraft).Count(&count)
	assert.Equal(t, int64(1), count)
}

// AC-07 — items without a supplier are skipped (no PO can be addressed).
func TestPurchaseOrdersFromReorder_SkipsUnlinkedItems(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	// A low-stock insumo with NO supplier.
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 1, MinStock: 5,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/from-reorder", nil)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data []models.PurchaseOrder `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data, "an unlinked low-stock item cannot become a PO")
}

// DELETE on a non-existent PO is a 404.
func TestDeletePurchaseOrder_NotFound(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodDelete,
		"/purchase-orders/99999999-9999-4999-8999-999999999999", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// PATCH on a non-existent PO is a 404.
func TestUpdatePurchaseOrder_NotFound(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch,
		"/purchase-orders/99999999-9999-4999-8999-999999999999",
		map[string]any{"notes": "x"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Sending a non-existent PO is a 404.
func TestSendPurchaseOrder_NotFound(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost,
		"/purchase-orders/99999999-9999-4999-8999-999999999999/send", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// PATCH notes-only on a draft PO is allowed and persists.
func TestUpdatePurchaseOrder_NotesOnly(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "1d000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/purchase-orders/"+poID, map[string]any{
		"notes": "Llega el martes",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var po models.PurchaseOrder
	require.NoError(t, db.First(&po, "id = ?", poID).Error)
	assert.Equal(t, "Llega el martes", po.Notes)
}

// PATCH with an unknown status string is a 400.
func TestUpdatePurchaseOrder_RejectsUnknownStatus(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "1d000000-0000-4000-8000-000000000002"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderDraft,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/purchase-orders/"+poID, map[string]any{
		"status": "pendiente",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ListPurchaseOrders rejects an invalid ?status= filter.
func TestListPurchaseOrders_RejectsInvalidStatusFilter(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodGet, "/purchase-orders?status=bogus", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// AC-07 — from-reorder with no low-stock items returns an empty list.
func TestPurchaseOrdersFromReorder_NothingLowStock(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Arroz", Unit: "kg",
		Stock: 50, MinStock: 5, SupplierID: ptrStr("sup-1"),
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/from-reorder", nil)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data []models.PurchaseOrder `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data, "nothing low → no PO")
}

// AC-07 — a low-stock item whose supplier was deleted is skipped.
func TestPurchaseOrdersFromReorder_SkipsDeletedSupplier(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	// Reference a supplier id that never existed.
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Arroz", Unit: "kg",
		Stock: 1, MinStock: 5, SupplierID: ptrStr("ghost-supplier"),
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/from-reorder", nil)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data []models.PurchaseOrder `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data, "an item pointing at a non-existent supplier yields no PO")
}

// FR-05 — receiving an empty PO via HTTP is a 422.
func TestReceivePurchaseOrderHandler_EmptyPOIs422(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	poID := "1e000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/receive", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

// §9 — receiving a PO whose item references a deleted insumo is a 422.
func TestReceivePurchaseOrderHandler_DeletedReferenceIs422(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-a", "sup-1", "ABC")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 3}
	require.NoError(t, db.Create(&ing).Error)
	poID := "1e000000-0000-4000-8000-000000000002"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-a", SupplierID: "sup-1", Status: models.PurchaseOrderSent,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, IngredientID: &ing.ID, NameSnapshot: "Arroz", Quantity: 5, UnitCost: 2900},
		},
	}).Error)
	require.NoError(t, db.Delete(&models.Ingredient{}, "id = ?", ing.ID).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/receive", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

// Art. III — receiving / reading another tenant's PO is a 404.
func TestPurchaseOrder_ForeignTenant404(t *testing.T) {
	db := setupPurchaseOrdersDB(t)
	seedPOSupplier(t, db, "tenant-b", "sup-b", "ABC")
	poID := "pocccccc-cccc-4ccc-8ccc-cccccccccccc"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: poID},
		TenantID:  "tenant-b", SupplierID: "sup-b", Status: models.PurchaseOrderDraft,
	}).Error)

	r := mountPurchaseOrders(db, "tenant-a")
	assert.Equal(t, http.StatusNotFound,
		doJSON(t, r, http.MethodGet, "/purchase-orders/"+poID, nil).Code)
	assert.Equal(t, http.StatusNotFound,
		doJSON(t, r, http.MethodPost, "/purchase-orders/"+poID+"/receive", nil).Code)
}
