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

// setupSaleCreditDB hand-crafts the SQLite schema needed for the
// credit-sale guard test. The schema mirrors branch_isolation_test
// but adds credit_accounts so we can verify the guard fires BEFORE
// any sale or credit row is touched.
func setupSaleCreditDB(t *testing.T) *gorm.DB {
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
		`CREATE TABLE customers (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '', phone TEXT DEFAULT '',
			email TEXT DEFAULT ''
		)`,
		`CREATE TABLE credit_accounts (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			customer_id TEXT NOT NULL,
			total_amount INTEGER NOT NULL DEFAULT 0,
			paid_amount INTEGER DEFAULT 0,
			status TEXT DEFAULT 'open'
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

// TestCreateSale_RejectsCreditWithoutAccount verifies the new guard:
// a credit (fiado) sale that doesn't carry credit_account_id must
// 400 instead of silently creating a CreditAccount the customer
// never authorized. This is the bug the audit traced back to
// sales.go:296-311 — the implicit-create path that produced rogue
// ledger accounts.
func TestCreateSale_RejectsCreditWithoutAccount(t *testing.T) {
	db := setupSaleCreditDB(t)

	tenantID := "tenant-creditguard"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, ?, 1)`,
		"55555555-5555-5555-5555-555555555555", time.Now(), time.Now(),
		tenantID, "Sede Única").Error)
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id, name, price, stock, is_available)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		"66666666-6666-6666-6666-666666666666", time.Now(), time.Now(),
		tenantID, "55555555-5555-5555-5555-555555555555",
		"Arroz", 3000, 50).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO customers (id, created_at, updated_at, tenant_id, name, phone)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"77777777-7777-7777-7777-777777777777", time.Now(), time.Now(),
		tenantID, "Don Carlos", "3001112222").Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Set(middleware.BranchIDKey, "55555555-5555-5555-5555-555555555555")
		c.Next()
	})
	r.POST("/api/v1/sales", handlers.CreateSale(db))

	customerID := "77777777-7777-7777-7777-777777777777"
	body := map[string]any{
		"payment_method": string(models.PaymentCredit),
		"customer_id":    customerID, // present, but no credit_account_id
		"items": []map[string]any{
			{"product_id": "66666666-6666-6666-6666-666666666666", "quantity": 1},
		},
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "credit_account_id requerido",
		"the guard message must reference credit_account_id so the client can fix the call")

	// And critically: NO sale row, NO credit_account row got written.
	var saleCount, creditCount int64
	require.NoError(t, db.Table("sales").Where("tenant_id = ?", tenantID).Count(&saleCount).Error)
	require.NoError(t, db.Table("credit_accounts").Where("tenant_id = ?", tenantID).Count(&creditCount).Error)
	assert.EqualValues(t, 0, saleCount, "no sale should be persisted when the guard fires")
	assert.EqualValues(t, 0, creditCount, "no credit_account should be created implicitly")
}
