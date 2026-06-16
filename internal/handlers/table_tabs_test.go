package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTableTabsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Tenant{},
		&models.OrderTicket{},
		&models.OrderItem{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// installTenantMiddleware injects a synthetic tenant + user id
// into the gin.Context so the handler's middleware.GetTenantID /
// GetUserID calls return something deterministic. Mirrors the
// approach used by the consent tests.
func installTenantMiddleware(tenantID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Set(middleware.UserIDKey, uuid.NewString())
		c.Next()
	}
}

func putJSON(r http.Handler, path string, body any) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestUpsertTableTab_CreatesTicketWithSessionTokenOnFirstSave(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.PUT("/api/v1/tables/tab", UpsertTableTab(db))

	w := putJSON(r, "/api/v1/tables/tab", map[string]any{
		"label": "Mesa 1",
		"items": []map[string]any{
			{
				"product_uuid": uuid.NewString(),
				"product_name": "Empanada",
				"quantity":     2,
				"unit_price":   5_000,
			},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	token, ok := body.Data["session_token"].(string)
	if !ok || token == "" {
		t.Fatalf("expected session_token in response, got %v", body.Data["session_token"])
	}
	if _, err := uuid.Parse(token); err != nil {
		t.Fatalf("session_token not a UUID: %q", token)
	}
	if got := body.Data["total"].(float64); got != 10_000 {
		t.Fatalf("total mismatch: %v", got)
	}

	var row models.OrderTicket
	if err := db.Where("label = ?", "Mesa 1").First(&row).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.SessionToken != token {
		t.Fatalf("persisted token mismatch: %q vs %q", row.SessionToken, token)
	}
	if row.Status != models.OrderStatusNuevo {
		t.Fatalf("expected status=nuevo, got %q", row.Status)
	}
}

func TestUpsertTableTab_UpdatesItemsAndPreservesSessionTokenOnSecondSave(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.PUT("/api/v1/tables/tab", UpsertTableTab(db))

	// Round 1 — creates.
	w1 := putJSON(r, "/api/v1/tables/tab", map[string]any{
		"label": "Mesa 5",
		"items": []map[string]any{
			{
				"product_uuid": "p1",
				"product_name": "Empanada",
				"quantity":     1,
				"unit_price":   5_000,
			},
		},
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("round 1: want 200, got %d body=%s", w1.Code, w1.Body.String())
	}
	var b1 struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w1.Body.Bytes(), &b1)
	firstToken := b1.Data["session_token"].(string)
	firstOrderID := b1.Data["order_id"].(string)

	// Round 2 — adds more items to the same label. Must REUSE the
	// same session_token AND the same order_id; total must reflect
	// the NEW cart (not be summed on top of the old one).
	w2 := putJSON(r, "/api/v1/tables/tab", map[string]any{
		"label": "Mesa 5",
		"items": []map[string]any{
			{
				"product_uuid": "p1",
				"product_name": "Empanada",
				"quantity":     2,
				"unit_price":   5_000,
			},
			{
				"product_uuid": "p2",
				"product_name": "Coca-Cola",
				"quantity":     1,
				"unit_price":   4_000,
			},
		},
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("round 2: want 200, got %d body=%s", w2.Code, w2.Body.String())
	}
	var b2 struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &b2)

	if b2.Data["session_token"].(string) != firstToken {
		t.Fatalf("session_token changed between rounds: %q → %q",
			firstToken, b2.Data["session_token"])
	}
	if b2.Data["order_id"].(string) != firstOrderID {
		t.Fatalf("order_id changed between rounds: %q → %q",
			firstOrderID, b2.Data["order_id"])
	}
	if got := b2.Data["total"].(float64); got != 14_000 {
		t.Fatalf("total mismatch after round 2: %v", got)
	}

	// And the DB has exactly ONE ticket for that label.
	var count int64
	db.Model(&models.OrderTicket{}).
		Where("tenant_id = ? AND label = ?", tenant.ID, "Mesa 5").
		Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 ticket for Mesa 5, got %d", count)
	}
	// With exactly 2 items (old row fully replaced).
	var items int64
	db.Model(&models.OrderItem{}).
		Where("order_uuid = ?", firstOrderID).
		Count(&items)
	if items != 2 {
		t.Fatalf("expected items=2 after replace, got %d", items)
	}
}

func TestUpsertTableTab_IsolatesTenants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDB(t)
	tA := seedTenant(t, db, uuid.NewString(), "a")
	tB := seedTenant(t, db, uuid.NewString(), "b")

	rA := gin.New()
	rA.Use(installTenantMiddleware(tA.ID))
	rA.PUT("/api/v1/tables/tab", UpsertTableTab(db))

	rB := gin.New()
	rB.Use(installTenantMiddleware(tB.ID))
	rB.PUT("/api/v1/tables/tab", UpsertTableTab(db))

	body := map[string]any{
		"label": "Mesa 1",
		"items": []map[string]any{
			{"product_uuid": "p", "product_name": "X", "quantity": 1, "unit_price": 100},
		},
	}

	wA := putJSON(rA, "/api/v1/tables/tab", body)
	wB := putJSON(rB, "/api/v1/tables/tab", body)
	if wA.Code != http.StatusOK || wB.Code != http.StatusOK {
		t.Fatalf("both should succeed independently")
	}

	// Two independent tickets, two independent tokens.
	var tokenA, tokenB string
	{
		var v struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(wA.Body.Bytes(), &v)
		tokenA = v.Data["session_token"].(string)
	}
	{
		var v struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(wB.Body.Bytes(), &v)
		tokenB = v.Data["session_token"].(string)
	}
	if tokenA == tokenB {
		t.Fatalf("session tokens should differ between tenants")
	}

	var countA, countB int64
	db.Model(&models.OrderTicket{}).Where("tenant_id = ?", tA.ID).Count(&countA)
	db.Model(&models.OrderTicket{}).Where("tenant_id = ?", tB.ID).Count(&countB)
	if countA != 1 || countB != 1 {
		t.Fatalf("expected 1 ticket per tenant, got A=%d B=%d", countA, countB)
	}
}

func TestUpsertTableTab_BadPayloads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.PUT("/api/v1/tables/tab", UpsertTableTab(db))

	cases := []struct {
		name string
		body any
	}{
		{
			name: "missing label",
			body: map[string]any{
				"items": []map[string]any{
					{"product_uuid": "p", "product_name": "X",
						"quantity": 1, "unit_price": 100},
				},
			},
		},
		{
			name: "blank label",
			body: map[string]any{
				"label": "   ",
				"items": []map[string]any{
					{"product_uuid": "p", "product_name": "X",
						"quantity": 1, "unit_price": 100},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := putJSON(r, "/api/v1/tables/tab", c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGetTableTab_ReturnsSessionTokenForOpenTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	// Seed an open ticket directly (no upsert needed for this test).
	order := models.OrderTicket{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		TenantID:  tenant.ID,
		Label:     "Mesa 9",
		Status:    models.OrderStatusNuevo,
		Type:      models.OrderTypeMesa,
		Total:     12_000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	var fresh models.OrderTicket
	db.First(&fresh, "id = ?", order.ID)

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.GET("/api/v1/tables/tab/:label", GetTableTab(db))

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/tables/tab/Mesa 9", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Data["session_token"].(string) != fresh.SessionToken {
		t.Fatalf("token mismatch: %v vs %v",
			body.Data["session_token"], fresh.SessionToken)
	}
}

func TestGetTableTab_404WhenNoOpenTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.GET("/api/v1/tables/tab/:label", GetTableTab(db))

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/tables/tab/Mesa 42", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func setupTableTabsDBFull(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Tenant{},
		&models.OrderTicket{},
		&models.OrderItem{},
		&models.Product{},
		&models.InventoryMovement{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// Spec 052: agregar ítems a un tab NO descuenta stock (el descuento ocurre una
// sola vez al cobrar, vía CloseOrder). Antes descontaba acá Y al cerrar = doble.
func TestAddItemsToTableTab_DoesNotDeductStock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDBFull(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	productID := uuid.NewString()
	db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenant.ID,
		Name:      "Empanada",
		Price:     2500,
		Stock:     10,
	})

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.POST("/api/v1/tables/tab/add-items", AddItemsToTableTab(db))

	w := postJSON(r, "/api/v1/tables/tab/add-items", map[string]any{
		"label": "Mesa 7",
		"items": []map[string]any{
			{"product_uuid": productID, "product_name": "Empanada", "quantity": 3, "unit_price": 2500},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)

	// Total = 3 * 2500 = 7500
	if got := body.Data["total"].(float64); got != 7500 {
		t.Fatalf("total mismatch: want 7500, got %v", got)
	}
	// Session token must exist
	if token, ok := body.Data["session_token"].(string); !ok || token == "" {
		t.Fatal("missing session_token")
	}
	// Item count = 1 (consolidated)
	items := body.Data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("item rows: want 1, got %d", len(items))
	}
	// AC-01: el stock NO se movió al agregar (sigue en 10).
	var p models.Product
	db.First(&p, "id = ?", productID)
	if p.Stock != 10 {
		t.Fatalf("stock debió quedar 10 (sin descuento al agregar), got %d", p.Stock)
	}
}

func TestAddItemsToTableTab_AccumulatesOnExistingTab(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDBFull(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	p1 := uuid.NewString()
	p2 := uuid.NewString()
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: p1}, TenantID: tenant.ID, Name: "Empanada", Price: 2500, Stock: 20})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: p2}, TenantID: tenant.ID, Name: "Coca Cola", Price: 3000, Stock: 15})

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.POST("/api/v1/tables/tab/add-items", AddItemsToTableTab(db))

	// Round 1: 2x Empanada
	w1 := postJSON(r, "/api/v1/tables/tab/add-items", map[string]any{
		"label": "Mesa 8",
		"items": []map[string]any{
			{"product_uuid": p1, "product_name": "Empanada", "quantity": 2, "unit_price": 2500},
		},
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("round 1: %d %s", w1.Code, w1.Body.String())
	}

	// Round 2: 1x Coca Cola + 1x more Empanada
	w2 := postJSON(r, "/api/v1/tables/tab/add-items", map[string]any{
		"label": "Mesa 8",
		"items": []map[string]any{
			{"product_uuid": p2, "product_name": "Coca Cola", "quantity": 1, "unit_price": 3000},
			{"product_uuid": p1, "product_name": "Empanada", "quantity": 1, "unit_price": 2500},
		},
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("round 2: %d %s", w2.Code, w2.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w2.Body.Bytes(), &body)

	// Total = (2+1)*2500 + 1*3000 = 7500 + 3000 = 10500
	if got := body.Data["total"].(float64); got != 10500 {
		t.Fatalf("total after accumulate: want 10500, got %v", got)
	}
	// Stock deduction verified via real Supabase DB (GREATEST is Postgres-only).
	// Only 1 ticket for Mesa 8
	var ticketCount int64
	db.Model(&models.OrderTicket{}).Where("tenant_id = ? AND label = ?", tenant.ID, "Mesa 8").Count(&ticketCount)
	if ticketCount != 1 {
		t.Fatalf("ticket count: want 1, got %d", ticketCount)
	}
	// 2 item rows (empanada consolidated, coca cola separate)
	items := body.Data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("item rows: want 2, got %d", len(items))
	}
}

// Spec 052: quitar un ítem del tab NO restaura stock (porque agregar tampoco lo
// descontó). El stock solo se mueve al cobrar.
func TestRemoveItemFromTab_DoesNotRestoreStock_RecalcTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableTabsDBFull(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	p1 := uuid.NewString()
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: p1}, TenantID: tenant.ID, Name: "Empanada", Price: 2500, Stock: 10})

	r := gin.New()
	r.Use(installTenantMiddleware(tenant.ID))
	r.POST("/api/v1/tables/tab/add-items", AddItemsToTableTab(db))
	r.DELETE("/api/v1/orders/:uuid/items/:item_id", RemoveItemFromTab(db))

	// Create tab with 3x Empanada
	w1 := postJSON(r, "/api/v1/tables/tab/add-items", map[string]any{
		"label": "Mesa 10",
		"items": []map[string]any{
			{"product_uuid": p1, "product_name": "Empanada", "quantity": 3, "unit_price": 2500},
		},
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w1.Code, w1.Body.String())
	}
	var b1 struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w1.Body.Bytes(), &b1)
	orderID := b1.Data["order_id"].(string)

	// Get the item ID
	var item models.OrderItem
	db.Where("order_uuid = ?", orderID).First(&item)

	// Delete the item
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/orders/"+orderID+"/items/"+item.ID, nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w2.Code, w2.Body.String())
	}

	// Total recalculated to 0 (no items left)
	var order models.OrderTicket
	db.First(&order, "id = ?", orderID)
	if order.Total != 0 {
		t.Fatalf("total after delete: want 0, got %v", order.Total)
	}
	// AC-02: el stock sigue intacto (ni se descontó al agregar ni se restauró).
	var p models.Product
	db.First(&p, "id = ?", p1)
	if p.Stock != 10 {
		t.Fatalf("stock debió quedar 10 (sin movimientos por el tab), got %d", p.Stock)
	}
}
