package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// Phase-6 branch isolation — the contract under test:
//
//  1. A tenant with two sedes keeps an independent stock counter on
//     each branch's copy of the same product (same UUID family,
//     different rows). A sale registered at Sede A MUST only
//     decrement Sede A's stock. Sede B stays untouched.
//  2. A sede-scoped list call (?branch_id=...) returns only that
//     sede's inventory rows.
//  3. A crafted request that tries to scope into another tenant's
//     sede gets a 403 { error_code: "branch_not_owned" }.
//
// The SQLite schema is hand-crafted (same pattern as branches_test
// and admin_login_test) because the production models carry
// gen_random_uuid() / jsonb defaults that sqlite can't parse.

func setupIsolationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	stmts := []string{
		`CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '', phone TEXT DEFAULT '',
			created_at DATETIME
		)`,
		`CREATE TABLE branches (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL, address TEXT DEFAULT '',
			is_active INTEGER DEFAULT 1
		)`,
		`CREATE TABLE products (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			branch_id TEXT,
			name TEXT NOT NULL, price REAL NOT NULL DEFAULT 0,
			stock INTEGER NOT NULL DEFAULT 0,
			is_available INTEGER DEFAULT 1,
			requires_container INTEGER DEFAULT 0,
			container_price INTEGER DEFAULT 0,
			purchase_price REAL DEFAULT 0,
			is_price_pending INTEGER DEFAULT 0,
			barcode TEXT DEFAULT '',
			image_url TEXT DEFAULT '',
			presentation TEXT DEFAULT '',
			content TEXT DEFAULT '',
			ingestion_method TEXT DEFAULT 'manual',
			expiry_date DATETIME
		)`,
		`CREATE TABLE sales (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			branch_id TEXT, created_by TEXT,
			employee_uuid TEXT, employee_name TEXT DEFAULT '',
			receipt_number INTEGER DEFAULT 0,
			total REAL NOT NULL DEFAULT 0,
			tax_amount REAL DEFAULT 0,
			tip_amount REAL DEFAULT 0,
			payment_method TEXT NOT NULL,
			customer_id TEXT, customer_name_snapshot TEXT DEFAULT '',
			customer_phone_snapshot TEXT DEFAULT '',
			is_credit INTEGER DEFAULT 0,
			credit_account_id TEXT,
			payment_status TEXT DEFAULT 'COMPLETED',
			dynamic_qr_payload TEXT,
			source TEXT NOT NULL DEFAULT 'POS'
		)`,
		`CREATE TABLE sale_items (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, sale_id TEXT NOT NULL,
			product_id TEXT, name TEXT NOT NULL,
			price REAL NOT NULL DEFAULT 0,
			quantity INTEGER NOT NULL,
			subtotal REAL NOT NULL DEFAULT 0,
			is_container_charge INTEGER DEFAULT 0,
			is_service INTEGER DEFAULT 0,
			custom_description TEXT DEFAULT '',
			custom_unit_price REAL DEFAULT 0
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

func seedBranchForIso(t *testing.T, db *gorm.DB, id, tenantID, name string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, ?, 1)`,
		id, time.Now(), time.Now(), tenantID, name).Error)
}

func seedProductAtBranch(t *testing.T, db *gorm.DB, id, tenantID, branchID, name string, stock int, price float64) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
		                     name, price, stock, is_available)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		id, time.Now(), time.Now(), tenantID, branchID, name, price, stock).Error)
}

func mountSalesHandler(db *gorm.DB, tenantID, branchID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		if branchID != "" {
			c.Set(middleware.BranchIDKey, branchID)
		}
		c.Next()
	})
	r.POST("/api/v1/sales", handlers.CreateSale(db))
	r.GET("/api/v1/products", handlers.ListProducts(db))
	return r
}

// ── The critical isolation test ──────────────────────────────────────────────

func TestCreateSale_BranchIsolation_StockDecrementStaysInSelectedBranch(t *testing.T) {
	db := setupIsolationDB(t)

	tenantID := "tenant-multi"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	seedBranchForIso(t, db, "11111111-1111-1111-1111-111111111111", tenantID, "Sede Norte")
	seedBranchForIso(t, db, "22222222-2222-2222-2222-222222222222", tenantID, "Sede Sur")

	// Each sede holds its own copy of "Gaseosa" with independent
	// stock. Same logical product, two physical rows — one per sede.
	seedProductAtBranch(t, db, "c1111111-1111-1111-1111-111111111111", tenantID, "11111111-1111-1111-1111-111111111111",
		"Gaseosa Cola", 50, 2500)
	seedProductAtBranch(t, db, "c2222222-2222-2222-2222-222222222222", tenantID, "22222222-2222-2222-2222-222222222222",
		"Gaseosa Cola", 30, 2500)

	// Simulate an employee of Sede Norte logging a sale of 3 bottles.
	// The JWT carries the cashier's workspace claim = br-norte.
	r := mountSalesHandler(db, tenantID, "11111111-1111-1111-1111-111111111111")

	body := map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      "11111111-1111-1111-1111-111111111111",
		"items": []map[string]any{
			{"product_id": "c1111111-1111-1111-1111-111111111111", "quantity": 3},
		},
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// Stock of Sede Norte: decremented from 50 → 47.
	var norte models.Product
	require.NoError(t, db.Where("id = ?", "c1111111-1111-1111-1111-111111111111").First(&norte).Error)
	assert.Equal(t, 47, norte.Stock,
		"Sede Norte's stock must drop from 50 → 47 after selling 3")

	// Stock of Sede Sur: UNCHANGED. This is the whole point of
	// Phase-6 isolation — a sale in A never touches B's counter.
	var sur models.Product
	require.NoError(t, db.Where("id = ?", "c2222222-2222-2222-2222-222222222222").First(&sur).Error)
	assert.Equal(t, 30, sur.Stock,
		"Sede Sur's stock must be unchanged — cross-sede stock bleed is the bug this test guards")
}

func TestCreateSale_RejectsSaleTargetingForeignBranch(t *testing.T) {
	// A crafted payload tries to attach a sale to another tenant's
	// sede. The ownership check must 403 before any row writes.
	db := setupIsolationDB(t)

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES
		('tenant-a', ?), ('tenant-b', ?)`, time.Now(), time.Now()).Error)
	seedBranchForIso(t, db, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "tenant-a", "Sede A")
	seedBranchForIso(t, db, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "tenant-b", "Sede B")
	seedProductAtBranch(t, db, "caaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "tenant-a", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "Producto A", 10, 1000)

	r := mountSalesHandler(db, "tenant-a", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	body := map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", // tenant-b's sede — must be rejected
		"items": []map[string]any{
			{"product_id": "caaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "quantity": 1},
		},
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "branch_not_owned")

	// Inventory untouched because the handler bailed before the
	// transaction opened.
	var p models.Product
	require.NoError(t, db.Where("id = ?", "caaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa").First(&p).Error)
	assert.Equal(t, 10, p.Stock)
}

func TestListProducts_BranchIsolation_ScopedListOnlyReturnsOwnBranch(t *testing.T) {
	db := setupIsolationDB(t)

	tenantID := "tenant-mb"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	seedBranchForIso(t, db, "33333333-3333-3333-3333-333333333333", tenantID, "Uno")
	seedBranchForIso(t, db, "44444444-4444-4444-4444-444444444444", tenantID, "Dos")

	seedProductAtBranch(t, db, "c3333333-3333-3333-3333-333333333333", tenantID, "33333333-3333-3333-3333-333333333333", "Arroz", 10, 2900)
	seedProductAtBranch(t, db, "c4444444-3333-3333-3333-333333333333", tenantID, "33333333-3333-3333-3333-333333333333", "Aceite", 5, 6500)
	seedProductAtBranch(t, db, "c5555555-4444-4444-4444-444444444444", tenantID, "44444444-4444-4444-4444-444444444444", "Agua", 40, 1800)

	// No branch in JWT — client picks via ?branch_id=.
	r := mountSalesHandler(db, tenantID, "")

	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/products?branch_id=33333333-3333-3333-3333-333333333333", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []models.Product `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data, 2,
		"only br-1's products should surface when scoped to that sede")
	names := []string{body.Data[0].Name, body.Data[1].Name}
	assert.Contains(t, names, "Arroz")
	assert.Contains(t, names, "Aceite")
	assert.NotContains(t, names, "Agua",
		"br-2's product must be invisible to a br-1-scoped query")
}

func TestListProducts_NoBranchFilter_PreservesBackwardCompat(t *testing.T) {
	// The pre-Phase-6 behaviour for mono-sede tenants: no branch
	// claim in JWT, no ?branch_id= in URL → return every product.
	// The regression this guards against is accidentally hiding
	// inventory from legacy tenants who never picked a sede.
	db := setupIsolationDB(t)

	tenantID := "tenant-legacy"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	seedBranchForIso(t, db, "55555555-5555-5555-5555-555555555555", tenantID, "Única")
	seedProductAtBranch(t, db, "d1111111-5555-5555-5555-555555555555", tenantID, "55555555-5555-5555-5555-555555555555", "Arroz", 10, 2900)
	seedProductAtBranch(t, db, "d2222222-5555-5555-5555-555555555555", tenantID, "55555555-5555-5555-5555-555555555555", "Aceite", 5, 6500)

	r := mountSalesHandler(db, tenantID, "") // no branch in JWT

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/products", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []models.Product `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Len(t, body.Data, 2,
		"mono-sede tenants without any branch context must still see everything")
}

func TestListProducts_ForeignBranchInQueryStringReturns403(t *testing.T) {
	db := setupIsolationDB(t)
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES
		('tenant-a', ?), ('tenant-b', ?)`, time.Now(), time.Now()).Error)
	seedBranchForIso(t, db, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "tenant-a", "A")
	seedBranchForIso(t, db, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "tenant-b", "B")
	seedProductAtBranch(t, db, "dbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "tenant-b", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "Secret", 99, 1)

	r := mountSalesHandler(db, "tenant-a", "")

	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/products?branch_id=bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", nil) // enemy sede
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "branch_not_owned")
}
