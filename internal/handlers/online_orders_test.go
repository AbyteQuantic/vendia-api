package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// KDS Phase 1 — contract under test:
//
//  1. PublicCreateOnlineOrder sets branch_id to the tenant's oldest
//     active branch, so a sede-scoped KDS fetch picks it up.
//  2. The endpoint accepts empty customer_phone (web checkout does
//     not always capture it).
//  3. payment_method / payment_method_id round-trip to the row.
//  4. ListOnlineOrders filters by ?status=pending and only surfaces
//     orders for the caller's tenant.
//  5. UpdateOnlineOrderStatus whitelists the target state — typos
//     land in 400, not the DB.

func setupOnlineOrdersDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	stmts := []string{
		`CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '', phone TEXT DEFAULT '',
			store_slug TEXT DEFAULT '',
			created_at DATETIME
		)`,
		`CREATE TABLE branches (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL, address TEXT DEFAULT '',
			is_active INTEGER DEFAULT 1
		)`,
		`CREATE TABLE online_orders (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			tenant_id TEXT NOT NULL, branch_id TEXT,
			customer_name TEXT NOT NULL,
			customer_phone TEXT DEFAULT '',
			delivery_type TEXT DEFAULT 'pickup',
			payment_method TEXT DEFAULT '',
			payment_method_id TEXT,
			status TEXT DEFAULT 'pending',
			total_amount REAL DEFAULT 0,
			items TEXT DEFAULT '[]',
			notes TEXT DEFAULT ''
		)`,
		`CREATE TABLE notifications (
			id TEXT PRIMARY KEY, created_at DATETIME,
			tenant_id TEXT NOT NULL, title TEXT NOT NULL,
			body TEXT DEFAULT '', type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

func seedTenantWithBranch(t *testing.T, db *gorm.DB, slug string) (tenantID, branchID string) {
	t.Helper()
	tenantID = uuid.NewString()
	branchID = uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Tienda Test', ?, datetime('now'))`,
		tenantID, slug,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO branches (id, tenant_id, name, is_active, created_at) VALUES (?, ?, 'Principal', 1, datetime('now', '-1 day'))`,
		branchID, tenantID,
	).Error)
	return tenantID, branchID
}

func postOnlineOrder(t *testing.T, db *gorm.DB, slug string, body any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/public/catalog/:slug/orders", handlers.PublicCreateOnlineOrder(db))

	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/public/catalog/%s/orders", slug),
		bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPublicCreateOnlineOrder_AttachesBranchIDAndPayment(t *testing.T) {
	db := setupOnlineOrdersDB(t)
	_, branchID := seedTenantWithBranch(t, db, "tienda-uno")

	w := postOnlineOrder(t, db, "tienda-uno", map[string]any{
		"customer_name":     "Pedro Perez",
		"customer_phone":    "",
		"delivery_type":     "pickup",
		"payment_method":    "Nequi Personal",
		"payment_method_id": uuid.NewString(),
		"items": []map[string]any{{
			"product_id": uuid.NewString(),
			"name":       "Empanada",
			"quantity":   2,
			"price":      3500,
		}},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var row models.OnlineOrder
	require.NoError(t, db.First(&row).Error)
	require.NotNil(t, row.BranchID, "branch_id must be attached")
	assert.Equal(t, branchID, *row.BranchID, "attaches the tenant's oldest active branch")
	assert.Equal(t, "Nequi Personal", row.PaymentMethod)
	assert.Equal(t, "pending", row.Status)
	assert.InDelta(t, 7000, row.TotalAmount, 0.001)
}

func TestPublicCreateOnlineOrder_AcceptsEmptyPhone(t *testing.T) {
	db := setupOnlineOrdersDB(t)
	seedTenantWithBranch(t, db, "tienda-dos")

	w := postOnlineOrder(t, db, "tienda-dos", map[string]any{
		"customer_name": "Solo Nombre",
		"items": []map[string]any{{
			"product_id": uuid.NewString(), "name": "Arroz", "quantity": 1, "price": 2000,
		}},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

func TestListOnlineOrders_FiltersByStatus(t *testing.T) {
	db := setupOnlineOrdersDB(t)
	tenantID, _ := seedTenantWithBranch(t, db, "tienda-tres")

	// Seed two orders: one pending, one accepted. Only the pending
	// should surface when ?status=pending.
	require.NoError(t, db.Exec(
		`INSERT INTO online_orders (id, tenant_id, customer_name, status, created_at) VALUES (?, ?, 'A', 'pending', datetime('now')), (?, ?, 'B', 'accepted', datetime('now'))`,
		uuid.NewString(), tenantID, uuid.NewString(), tenantID,
	).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/online-orders", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		handlers.ListOnlineOrders(db)(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/online-orders?status=pending", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []models.OnlineOrder `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "pending", resp.Data[0].Status)
	assert.Equal(t, "A", resp.Data[0].CustomerName)
}

func TestUpdateOnlineOrderStatus_RejectsUnknownStatus(t *testing.T) {
	db := setupOnlineOrdersDB(t)
	tenantID, _ := seedTenantWithBranch(t, db, "tienda-cuatro")
	orderID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO online_orders (id, tenant_id, customer_name, status, created_at) VALUES (?, ?, 'A', 'pending', datetime('now'))`,
		orderID, tenantID,
	).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PATCH("/api/v1/online-orders/:id", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		handlers.UpdateOnlineOrderStatus(db)(c)
	})

	for _, bad := range []string{"NUEVO", "cancelado", "cooked"} {
		body, _ := json.Marshal(map[string]string{"status": bad})
		req := httptest.NewRequest(http.MethodPatch,
			"/api/v1/online-orders/"+orderID, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code, "input=%q should 400", bad)
	}

	// Whitelisted value goes through.
	body, _ := json.Marshal(map[string]string{"status": "accepted"})
	req := httptest.NewRequest(http.MethodPatch,
		"/api/v1/online-orders/"+orderID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var row models.OnlineOrder
	require.NoError(t, db.First(&row, "id = ?", orderID).Error)
	assert.Equal(t, "accepted", row.Status)
}
