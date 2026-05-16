// Spec: specs/003-trabajos-muebles/spec.md
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

// setupWorkOrdersDB opens an in-memory sqlite DB with every table the
// work-order handlers touch. The Tenant model's
// `default:gen_random_uuid()` breaks AutoMigrate on sqlite, so the
// tenants table is hand-crafted (same trick as purchase_orders_test).
func setupWorkOrdersDB(t *testing.T) *gorm.DB {
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
		&models.WorkOrder{},
		&models.WorkOrderItem{},
		&models.WorkOrderPayment{},
		&models.Ingredient{},
		&models.Product{},
		&models.Customer{},
		&models.InventoryMovement{},
	))
	return db
}

func mountWorkOrders(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.GET("/work-orders", handlers.ListWorkOrders(db))
	r.POST("/work-orders", handlers.CreateWorkOrder(db))
	r.GET("/work-orders/:uuid", handlers.GetWorkOrder(db))
	r.PATCH("/work-orders/:uuid", handlers.UpdateWorkOrder(db))
	r.DELETE("/work-orders/:uuid", handlers.DeleteWorkOrder(db))
	r.POST("/work-orders/:uuid/payments", handlers.CreateWorkOrderPayment(db))
	r.POST("/work-orders/:uuid/share", handlers.ShareWorkOrder(db))
	return r
}

func seedWOCustomer(t *testing.T, db *gorm.DB, tenantID, customerID, name string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, owner_name, created_at)
		VALUES (?, 'Carpintería Test', 'Don Pepe', ?) ON CONFLICT(id) DO NOTHING`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Create(&models.Customer{
		BaseModel: models.BaseModel{ID: customerID},
		TenantID:  tenantID, Name: name, Phone: "3001234567",
	}).Error)
}

// woResponse mirrors the {data: WorkOrder} envelope.
type woResponse struct {
	Data models.WorkOrder `json:"data"`
}

// AC-01 — create a work order with 1 material (Madera ×2 @20000) and 1
// labour (@50000), then read it back: the computed total is 90000.
func TestCreateWorkOrder_PersistsWithItemsAndTotal(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Madera", Unit: "unidad", Stock: 10}
	require.NoError(t, db.Create(&ing).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
		"customer_id": "cust-1",
		"type":        models.WorkOrderTypeFabrication,
		"description": "Mesa de comedor a la medida",
		"items": []map[string]any{
			{"kind": "material", "ingredient_id": ing.ID, "description": "Madera", "quantity": 2, "unit_price": 20000},
			{"kind": "mano_obra", "description": "Mano de obra", "quantity": 1, "unit_price": 50000},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp woResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "cust-1", resp.Data.CustomerID)
	assert.Equal(t, models.WorkOrderQuote, resp.Data.Status)
	assert.Len(t, resp.Data.Items, 2)
	assert.Equal(t, float64(90000), resp.Data.Total, "AC-01 — total = 2*20000 + 50000")

	// Read it back via GET.
	w2 := doJSON(t, r, http.MethodGet, "/work-orders/"+resp.Data.ID, nil)
	require.Equal(t, http.StatusOK, w2.Code)
	var got woResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &got))
	assert.Equal(t, float64(90000), got.Data.Total)
	assert.NotEmpty(t, got.Data.ID, "the model exposes the BaseModel ID as JSON id")
}

// FR-02 — a material item must reference an insumo XOR a product.
func TestCreateWorkOrder_RejectsMaterialWithBothOrNeitherRef(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	r := mountWorkOrders(db, "tenant-a")

	wBoth := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
		"customer_id": "cust-1", "type": "fabricacion",
		"items": []map[string]any{
			{"kind": "material", "ingredient_id": "i1", "product_id": "p1", "quantity": 1, "unit_price": 1},
		},
	})
	assert.Equal(t, http.StatusBadRequest, wBoth.Code, wBoth.Body.String())

	wNone := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
		"customer_id": "cust-1", "type": "fabricacion",
		"items": []map[string]any{
			{"kind": "material", "quantity": 1, "unit_price": 1},
		},
	})
	assert.Equal(t, http.StatusBadRequest, wNone.Code, wNone.Body.String())
}

// FR-02 invariant — a mano_obra item must NOT reference inventory.
func TestCreateWorkOrder_RejectsLabourWithInventoryRef(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	r := mountWorkOrders(db, "tenant-a")

	w := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
		"customer_id": "cust-1", "type": "fabricacion",
		"items": []map[string]any{
			{"kind": "mano_obra", "ingredient_id": "i1", "description": "X", "quantity": 1, "unit_price": 1},
		},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// FR-02 — quantity / unit price ≤ 0 is rejected.
func TestCreateWorkOrder_RejectsNonPositiveAmounts(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	r := mountWorkOrders(db, "tenant-a")

	for _, bad := range []map[string]any{
		{"kind": "mano_obra", "description": "X", "quantity": 0, "unit_price": 100},
		{"kind": "mano_obra", "description": "X", "quantity": 1, "unit_price": 0},
		{"kind": "mano_obra", "description": "X", "quantity": -1, "unit_price": 100},
	} {
		w := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
			"customer_id": "cust-1", "type": "fabricacion",
			"items": []map[string]any{bad},
		})
		assert.Equal(t, http.StatusBadRequest, w.Code, "item %v must 400", bad)
	}
}

// VI — customer_id is required and must exist for the tenant.
func TestCreateWorkOrder_RejectsMissingOrForeignCustomer(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	seedWOCustomer(t, db, "tenant-b", "cust-b", "Otro")
	r := mountWorkOrders(db, "tenant-a")

	wMissing := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
		"type":  "fabricacion",
		"items": []map[string]any{{"kind": "mano_obra", "description": "X", "quantity": 1, "unit_price": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, wMissing.Code)

	wForeign := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
		"customer_id": "cust-b", "type": "fabricacion",
		"items": []map[string]any{{"kind": "mano_obra", "description": "X", "quantity": 1, "unit_price": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, wForeign.Code)
}

// FR-01 — an unknown work order type is rejected.
func TestCreateWorkOrder_RejectsUnknownType(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
		"customer_id": "cust-1", "type": "bogus",
		"items": []map[string]any{{"kind": "mano_obra", "description": "X", "quantity": 1, "unit_price": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Idempotent create — re-sending the same client UUID does not duplicate.
func TestCreateWorkOrder_IdempotentByUUID(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	r := mountWorkOrders(db, "tenant-a")
	id := "71111111-1111-4111-8111-111111111111"

	for i := 0; i < 2; i++ {
		w := doJSON(t, r, http.MethodPost, "/work-orders", map[string]any{
			"id": id, "customer_id": "cust-1", "type": "reparacion",
			"items": []map[string]any{{"kind": "mano_obra", "description": "X", "quantity": 1, "unit_price": 100}},
		})
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.WorkOrder{}).Where("id = ?", id).Count(&count)
	assert.Equal(t, int64(1), count, "re-sending the same UUID must not duplicate")
}

// AC-05 — a valid transition (cotizacion → aprobada) is accepted and
// stamps approved_at.
func TestUpdateWorkOrder_ValidTransitionApproved(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "72222222-2222-4222-8222-222222222222"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderQuote, Total: 1000,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: "mano_obra", Description: "X", Quantity: 1, UnitPrice: 1000},
		},
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"status": models.WorkOrderApproved,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var wo models.WorkOrder
	require.NoError(t, db.First(&wo, "id = ?", woID).Error)
	assert.Equal(t, models.WorkOrderApproved, wo.Status)
	require.NotNil(t, wo.ApprovedAt, "approved_at must be stamped")
}

// AC-05 — an invalid transition (cotizacion → entregada) is rejected.
func TestUpdateWorkOrder_RejectsInvalidTransition(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "73333333-3333-4333-8333-333333333333"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderQuote,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"status": models.WorkOrderDelivered,
	})
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

// §9 — a work order with no items cannot be approved.
func TestUpdateWorkOrder_RejectsApproveWithoutItems(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "73333333-3333-4333-8333-3333330000ee"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderQuote,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"status": models.WorkOrderApproved,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

// AC-03 — transitioning to terminada via PATCH discounts material stock
// through the kardex and stamps completed_at.
func TestUpdateWorkOrder_TransitionToCompletedConsumesMaterial(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Madera", Unit: "unidad", Stock: 10}
	require.NoError(t, db.Create(&ing).Error)
	woID := "74444444-4444-4444-8444-444444444444"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderInProgress,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: "material", IngredientID: &ing.ID,
				Description: "Madera", Quantity: 2, UnitPrice: 20000},
		},
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"status": models.WorkOrderCompleted,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var madera models.Ingredient
	require.NoError(t, db.First(&madera, "id = ?", ing.ID).Error)
	assert.InDelta(t, 8.0, madera.Stock, 1e-9, "AC-03 — madera 10 - 2 = 8")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementWorkOrderConsumption).Count(&movCount)
	assert.Equal(t, int64(1), movCount, "one work_order_consumption movement")

	var wo models.WorkOrder
	require.NoError(t, db.First(&wo, "id = ?", woID).Error)
	require.NotNil(t, wo.CompletedAt, "completed_at must be stamped")
}

// AC-04 — a second PATCH to terminada on an already-terminada order is a
// safe no-op (re-sync); stock does not double-discount.
func TestUpdateWorkOrder_ReCompleteIsIdempotent(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Madera", Unit: "unidad", Stock: 10}
	require.NoError(t, db.Create(&ing).Error)
	woID := "74444444-4444-4444-8444-4444440000aa"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderInProgress,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: "material", IngredientID: &ing.ID,
				Description: "Madera", Quantity: 2, UnitPrice: 20000},
		},
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w1 := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{"status": models.WorkOrderCompleted})
	require.Equal(t, http.StatusOK, w1.Code)
	w2 := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{"status": models.WorkOrderCompleted})
	require.Equal(t, http.StatusOK, w2.Code, "re-completing must not error")

	var madera models.Ingredient
	require.NoError(t, db.First(&madera, "id = ?", ing.ID).Error)
	assert.InDelta(t, 8.0, madera.Stock, 1e-9, "AC-04 — stock must NOT double-discount")
}

// AC-07 — editing items of a terminada work order is rejected.
func TestUpdateWorkOrder_RejectsItemEditAfterApproved(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "75555555-5555-4555-8555-555555555555"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderCompleted,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: "mano_obra", Description: "X", Quantity: 1, UnitPrice: 1},
		},
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"items": []map[string]any{
			{"kind": "mano_obra", "description": "Y", "quantity": 2, "unit_price": 99},
		},
	})
	assert.Equal(t, http.StatusConflict, w.Code, "AC-07 — items frozen outside cotizacion/aprobada")
}

// FR-07 — editing items of an `aprobada` work order replaces them and
// recomputes the total.
func TestUpdateWorkOrder_ReplacesItemsAndRecomputesTotal(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "76666666-6666-4666-8666-666666666666"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderApproved, Total: 1000,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: "mano_obra", Description: "X", Quantity: 1, UnitPrice: 1000},
		},
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"items": []map[string]any{
			{"kind": "mano_obra", "description": "Y", "quantity": 1, "unit_price": 75000},
		},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var wo models.WorkOrder
	require.NoError(t, db.Preload("Items").First(&wo, "id = ?", woID).Error)
	assert.Len(t, wo.Items, 1)
	assert.Equal(t, float64(75000), wo.Total, "total recomputed")
}

// AC-02 — registering a 40000 advance against a 90000 work order yields
// abonado=40000 and saldo=50000.
func TestCreateWorkOrderPayment_RegistersAdvanceAndExposesBalance(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "77777777-7777-4777-8777-777777777777"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderApproved, Total: 90000,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/work-orders/"+woID+"/payments", map[string]any{
		"amount": 40000, "method": "efectivo",
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// The work order GET exposes total / abonado / saldo.
	w2 := doJSON(t, r, http.MethodGet, "/work-orders/"+woID, nil)
	require.Equal(t, http.StatusOK, w2.Code)
	var resp struct {
		Data struct {
			Total   float64 `json:"total"`
			Abonado float64 `json:"abonado"`
			Saldo   float64 `json:"saldo"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
	assert.Equal(t, float64(90000), resp.Data.Total)
	assert.Equal(t, float64(40000), resp.Data.Abonado, "AC-02 — abonado")
	assert.Equal(t, float64(50000), resp.Data.Saldo, "AC-02 — saldo")
}

// §7 — an advance cannot exceed the outstanding balance.
func TestCreateWorkOrderPayment_RejectsOverpayment(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "78888888-8888-4888-8888-888888888888"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderApproved, Total: 50000,
		Payments: []models.WorkOrderPayment{
			{TenantID: "tenant-a", WorkOrderID: woID, Amount: 30000},
		},
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	// Balance is 20000 — a 25000 advance must be rejected.
	w := doJSON(t, r, http.MethodPost, "/work-orders/"+woID+"/payments", map[string]any{
		"amount": 25000, "method": "efectivo",
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

	var count int64
	db.Model(&models.WorkOrderPayment{}).Where("work_order_id = ?", woID).Count(&count)
	assert.Equal(t, int64(1), count, "the rejected advance must not be persisted")
}

// A non-positive advance amount is rejected.
func TestCreateWorkOrderPayment_RejectsNonPositiveAmount(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "78888888-8888-4888-8888-8888880000bb"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderApproved, Total: 50000,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/work-orders/"+woID+"/payments", map[string]any{
		"amount": 0, "method": "efectivo",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// AC-06 — sharing a cotizacion work order returns a wa.me URL with the
// item breakdown and the total.
func TestShareWorkOrder_ReturnsWhatsAppURL(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "79999999-9999-4999-8999-999999999999"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderQuote, Total: 90000,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: "material", IngredientID: ptrStr("i1"),
				Description: "Madera", Quantity: 2, UnitPrice: 20000},
			{WorkOrderID: woID, Kind: "mano_obra", Description: "Mano de obra", Quantity: 1, UnitPrice: 50000},
		},
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/work-orders/"+woID+"/share", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			WhatsAppURL string `json:"whatsapp_url"`
			Message     string `json:"message"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Data.WhatsAppURL, "wa.me/")
	assert.Contains(t, resp.Data.Message, "Madera")
	assert.Contains(t, resp.Data.Message, "Mano de obra")
	assert.Contains(t, resp.Data.Message, "$90.000")
}

// AC-06 — sharing is only allowed while the order is a cotizacion.
func TestShareWorkOrder_RejectsNonQuoteStatus(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "7a000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderApproved, Total: 1000,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPost, "/work-orders/"+woID+"/share", nil)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

// DELETE soft-deletes the work order.
func TestDeleteWorkOrder_SoftDeletes(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "7b000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderQuote,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodDelete, "/work-orders/"+woID, nil)
	require.Equal(t, http.StatusOK, w.Code)

	var count int64
	db.Model(&models.WorkOrder{}).Where("id = ?", woID).Count(&count)
	assert.Equal(t, int64(0), count, "soft-deleted work order must not surface")
}

// Art. III — list is scoped to the tenant and filterable by status/type.
func TestListWorkOrders_TenantIsolationAndFilters(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	require.NoError(t, db.Create(&models.WorkOrder{
		TenantID: "tenant-a", CustomerID: "cust-1", Type: "fabricacion", Status: models.WorkOrderQuote,
	}).Error)
	require.NoError(t, db.Create(&models.WorkOrder{
		TenantID: "tenant-a", CustomerID: "cust-1", Type: "reparacion", Status: models.WorkOrderApproved,
	}).Error)
	require.NoError(t, db.Create(&models.WorkOrder{
		TenantID: "tenant-b", CustomerID: "cust-x", Type: "fabricacion", Status: models.WorkOrderQuote,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")

	wAll := doJSON(t, r, http.MethodGet, "/work-orders", nil)
	require.Equal(t, http.StatusOK, wAll.Code)
	var respAll handlers.PaginatedResponse
	require.NoError(t, json.Unmarshal(wAll.Body.Bytes(), &respAll))
	assert.Equal(t, int64(2), respAll.Total, "only tenant-a's work orders")

	wStatus := doJSON(t, r, http.MethodGet, "/work-orders?status=aprobada", nil)
	require.Equal(t, http.StatusOK, wStatus.Code)
	var respStatus handlers.PaginatedResponse
	require.NoError(t, json.Unmarshal(wStatus.Body.Bytes(), &respStatus))
	assert.Equal(t, int64(1), respStatus.Total, "only the aprobada work order")

	wType := doJSON(t, r, http.MethodGet, "/work-orders?type=reparacion", nil)
	require.Equal(t, http.StatusOK, wType.Code)
	var respType handlers.PaginatedResponse
	require.NoError(t, json.Unmarshal(wType.Body.Bytes(), &respType))
	assert.Equal(t, int64(1), respType.Total, "only the reparacion work order")
}

// ListWorkOrders rejects an invalid ?status= filter.
func TestListWorkOrders_RejectsInvalidStatusFilter(t *testing.T) {
	db := setupWorkOrdersDB(t)
	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodGet, "/work-orders?status=bogus", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Art. III — reading another tenant's work order is a 404.
func TestWorkOrder_ForeignTenant404(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-b", "cust-b", "Otro")
	woID := "7c000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-b", CustomerID: "cust-b",
		Type: "fabricacion", Status: models.WorkOrderQuote,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	assert.Equal(t, http.StatusNotFound,
		doJSON(t, r, http.MethodGet, "/work-orders/"+woID, nil).Code)
	assert.Equal(t, http.StatusNotFound,
		doJSON(t, r, http.MethodPost, "/work-orders/"+woID+"/share", nil).Code)
	assert.Equal(t, http.StatusNotFound,
		doJSON(t, r, http.MethodPost, "/work-orders/"+woID+"/payments",
			map[string]any{"amount": 1, "method": "efectivo"}).Code)
}

// PATCH / DELETE / payment / share on a non-existent work order are 404s.
func TestWorkOrder_NotFound(t *testing.T) {
	db := setupWorkOrdersDB(t)
	r := mountWorkOrders(db, "tenant-a")
	missing := "/work-orders/99999999-9999-4999-8999-999999999999"
	assert.Equal(t, http.StatusNotFound, doJSON(t, r, http.MethodGet, missing, nil).Code)
	assert.Equal(t, http.StatusNotFound,
		doJSON(t, r, http.MethodPatch, missing, map[string]any{"notes": "x"}).Code)
	assert.Equal(t, http.StatusNotFound, doJSON(t, r, http.MethodDelete, missing, nil).Code)
	assert.Equal(t, http.StatusNotFound, doJSON(t, r, http.MethodPost, missing+"/share", nil).Code)
}

// §9 / AC-05 — a PATCH to terminada whose material item references a
// soft-deleted insumo is rejected with 422 and leaves the order
// untouched (the handler maps the service error via completeErrorStatus).
func TestUpdateWorkOrder_CompleteWithDeletedReferenceIs422(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Madera", Unit: "unidad", Stock: 10}
	require.NoError(t, db.Create(&ing).Error)
	woID := "7e000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderInProgress,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: "material", IngredientID: &ing.ID,
				Description: "Madera", Quantity: 2, UnitPrice: 20000},
		},
	}).Error)
	require.NoError(t, db.Delete(&models.Ingredient{}, "id = ?", ing.ID).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"status": models.WorkOrderCompleted,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

	var wo models.WorkOrder
	require.NoError(t, db.First(&wo, "id = ?", woID).Error)
	assert.Equal(t, models.WorkOrderInProgress, wo.Status,
		"a blocked completion must leave the status untouched")
}

// AC-05 — a sold/cancelled (terminal) work order cannot transition.
func TestUpdateWorkOrder_RejectsTransitionFromTerminal(t *testing.T) {
	db := setupWorkOrdersDB(t)
	seedWOCustomer(t, db, "tenant-a", "cust-1", "Carlos")
	woID := "7d000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel: models.BaseModel{ID: woID},
		TenantID:  "tenant-a", CustomerID: "cust-1",
		Type: "fabricacion", Status: models.WorkOrderCancelled,
	}).Error)

	r := mountWorkOrders(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/work-orders/"+woID, map[string]any{
		"status": models.WorkOrderApproved,
	})
	assert.Equal(t, http.StatusConflict, w.Code)
}
