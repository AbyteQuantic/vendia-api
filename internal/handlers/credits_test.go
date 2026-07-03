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

// setupCreditsDB hand-crafts the SQLite schema needed for the credits
// list/group/close tests. Mirrors the production columns the handler
// references — no AutoMigrate because the production CreditAccount
// model uses Postgres-specific defaults.
func setupCreditsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	stmts := []string{
		`CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '', created_at DATETIME
		)`,
		`CREATE TABLE branches (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL, is_active INTEGER DEFAULT 1
		)`,
		`CREATE TABLE customers (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '', phone TEXT DEFAULT '',
			email TEXT DEFAULT '', notes TEXT DEFAULT '',
			marketing_opt_in INTEGER DEFAULT 0, terms_accepted INTEGER DEFAULT 0,
			terms_accepted_at DATETIME, last_order_at DATETIME
		)`,
		`CREATE TABLE credit_accounts (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			created_by TEXT, branch_id TEXT,
			customer_id TEXT NOT NULL,
			sale_id TEXT,
			total_amount INTEGER NOT NULL DEFAULT 0,
			paid_amount INTEGER DEFAULT 0,
			description TEXT DEFAULT '',
			status TEXT DEFAULT 'open',
			due_date DATETIME,
			closed_at DATETIME,
			fiado_token TEXT DEFAULT '',
			fiado_status TEXT DEFAULT 'none',
			accepted_at DATETIME,
			accepted_ip TEXT DEFAULT ''
		)`,
		`CREATE TABLE credit_payments (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, credit_account_id TEXT NOT NULL,
			created_by TEXT, branch_id TEXT,
			amount INTEGER NOT NULL DEFAULT 0,
			payment_method TEXT DEFAULT 'cash',
			note TEXT DEFAULT '',
			receipt_image_url TEXT DEFAULT ''
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

func mountCreditsList(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/api/v1/credits", handlers.ListCredits(db))
	return r
}

func mountCloseCredit(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/api/v1/credits/:id/close", handlers.CloseCredit(db, nil))
	return r
}

func mountCreditPayment(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/api/v1/credits/:id/payments", handlers.CreatePayment(db, nil))
	return r
}

// TestListCredits_GroupByCustomer_AggregatesPerCustomer asserts the
// new aggregation path: 3 fiados for one customer + 1 fiado for
// another collapse to 2 rows, and the balances/account counts are
// correct. This is what powers the cuaderno's "one row per debtor"
// view — without it the screen renders three near-identical entries
// for the same person.
func TestListCredits_GroupByCustomer_AggregatesPerCustomer(t *testing.T) {
	db := setupCreditsDB(t)
	tenantID := "tenant-grouping"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name, created_at) VALUES (?, ?, ?)`,
		tenantID, "Tienda Group", time.Now()).Error)

	custA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	custB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	require.NoError(t, db.Exec(`
		INSERT INTO customers (id, created_at, updated_at, tenant_id, name, phone) VALUES
		(?, datetime('now'), datetime('now'), ?, ?, ?),
		(?, datetime('now'), datetime('now'), ?, ?, ?)`,
		custA, tenantID, "Viviana", "3001111111",
		custB, tenantID, "Pedro", "3002222222").Error)

	// Mix of statuses for Viviana + 1 open for Pedro. The PO mandate
	// for the cuaderno's "Activos" tab is:
	//   * status ∈ {open, partial}, AND
	//   * fiado_status = accepted.
	// The pending row (status='pending', fiado_status='link_sent')
	// MUST be excluded from the rollup — it belongs to the
	// "Pendientes" tab and has not been accepted yet.
	now := time.Now()
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status, fiado_status)
		VALUES
			(?, ?, ?, ?, ?, 10000, 0,    'open',    'accepted'),
			(?, ?, ?, ?, ?, 20000, 5000, 'partial', 'accepted'),
			(?, ?, ?, ?, ?, 7000,  0,    'pending', 'link_sent'),
			(?, ?, ?, ?, ?, 30000, 0,    'open',    'accepted')
		`,
		"c1111111-1111-1111-1111-111111111111", now, now, tenantID, custA,
		"c2222222-2222-2222-2222-222222222222", now, now, tenantID, custA,
		"c3333333-3333-3333-3333-333333333333", now, now, tenantID, custA,
		"c4444444-4444-4444-4444-444444444444", now, now, tenantID, custB,
	).Error)

	// One paid account for Viviana that MUST be excluded from the rollup.
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status, fiado_status, closed_at)
		VALUES (?, ?, ?, ?, ?, 5000, 5000, 'paid', 'accepted', ?)`,
		"c5555555-5555-5555-5555-555555555555", now, now, tenantID, custA, now,
	).Error)

	r := mountCreditsList(db, tenantID)

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/credits?group_by=customer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data, 2,
		"two distinct customers must collapse to two rows (paid + pending excluded)")

	// Index by customer id so the test isn't order-sensitive.
	byCustomer := map[string]map[string]any{}
	for _, row := range body.Data {
		byCustomer[row["customer_id"].(string)] = row
	}

	// Viviana: 2 ACCEPTED accounts (the link_sent one was excluded),
	// total = 10k+20k = 30k, paid = 5k, balance = 25k.
	viv := byCustomer[custA]
	require.NotNil(t, viv, "Viviana's row must be present")
	assert.EqualValues(t, 30000, viv["total_amount"])
	assert.EqualValues(t, 5000, viv["paid_amount"])
	assert.EqualValues(t, 25000, viv["balance"])
	assert.EqualValues(t, 2, viv["accounts_count"],
		"the pending/link_sent row must NOT count toward accounts_count")
	assert.Equal(t, "open", viv["status"])
	assert.Equal(t, "Viviana", viv["customer_name"])

	// Pedro: 1 accepted+open account, balance = 30k.
	pedro := byCustomer[custB]
	require.NotNil(t, pedro, "Pedro's row must be present")
	assert.EqualValues(t, 30000, pedro["total_amount"])
	assert.EqualValues(t, 0, pedro["paid_amount"])
	assert.EqualValues(t, 30000, pedro["balance"])
	assert.EqualValues(t, 1, pedro["accounts_count"])
	assert.Equal(t, "open", pedro["status"])
}

// TestListCredits_GroupByCustomer_ExcludesNonAccepted is the negative
// counterpart: a customer whose only ledger row is in the handshake
// state must NOT appear in the Activos tab. Closes the "Tab Bleeding"
// regression where a link_sent fiado bled into both tabs.
func TestListCredits_GroupByCustomer_ExcludesNonAccepted(t *testing.T) {
	db := setupCreditsDB(t)
	tenantID := "tenant-bleed-fix"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name, created_at) VALUES (?, ?, ?)`,
		tenantID, "Tienda Bleed", time.Now()).Error)

	cust := "11111111-1111-1111-1111-aaaaaaaaaaaa"
	require.NoError(t, db.Exec(`
		INSERT INTO customers (id, created_at, updated_at, tenant_id, name, phone)
		VALUES (?, datetime('now'), datetime('now'), ?, ?, ?)`,
		cust, tenantID, "PendingOnly", "3009999999").Error)

	now := time.Now()
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status, fiado_status)
		VALUES
			(?, ?, ?, ?, ?, 8000, 0, 'pending', 'link_sent'),
			(?, ?, ?, ?, ?, 4000, 0, 'pending', 'link_opened')`,
		"d1111111-1111-1111-1111-111111111111", now, now, tenantID, cust,
		"d2222222-2222-2222-2222-222222222222", now, now, tenantID, cust,
	).Error)

	r := mountCreditsList(db, tenantID)
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/credits?group_by=customer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Empty(t, body.Data,
		"a customer with only link_sent/link_opened rows must NOT show in Activos")
}

// TestListCredits_StatusPending_FiltersByFiadoStatus pins the
// reciprocal rule: ?status=pending now means "fiado_status ∈
// {pending, link_sent, link_opened}" — the fiado handshake bucket —
// not the column-level status. A row whose column-level status is
// 'pending' but whose fiado_status is 'accepted' must NOT show up.
func TestListCredits_StatusPending_FiltersByFiadoStatus(t *testing.T) {
	db := setupCreditsDB(t)
	tenantID := "tenant-pending-bucket"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name, created_at) VALUES (?, ?, ?)`,
		tenantID, "Tienda Pending", time.Now()).Error)

	custA := "22222222-2222-2222-2222-aaaaaaaaaaaa"
	custB := "22222222-2222-2222-2222-bbbbbbbbbbbb"
	require.NoError(t, db.Exec(`
		INSERT INTO customers (id, created_at, updated_at, tenant_id, name, phone) VALUES
			(?, datetime('now'), datetime('now'), ?, ?, ?),
			(?, datetime('now'), datetime('now'), ?, ?, ?)`,
		custA, tenantID, "Awaiting", "3008888888",
		custB, tenantID, "Accepted", "3007777777").Error)

	now := time.Now()
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status, fiado_status)
		VALUES
			(?, ?, ?, ?, ?, 5000, 0, 'pending', 'link_sent'),
			(?, ?, ?, ?, ?, 9000, 0, 'open',    'accepted')`,
		"e1111111-1111-1111-1111-111111111111", now, now, tenantID, custA,
		"e2222222-2222-2222-2222-222222222222", now, now, tenantID, custB,
	).Error)

	r := mountCreditsList(db, tenantID)
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/credits?status=pending", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data, 1,
		"only the link_sent row should be in the Pendientes bucket")
	assert.Equal(t, custA, body.Data[0]["customer_id"])
}

// TestCloseCredit_StampsClosedAt verifies the timestamp lands on the
// row when an admin closes a credit (write-off path with force=true).
// Without it the "Pagados" tab can't sort by settlement date.
func TestCloseCredit_StampsClosedAt(t *testing.T) {
	db := setupCreditsDB(t)
	tenantID := "tenant-close"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name, created_at) VALUES (?, ?, ?)`,
		tenantID, "Tienda Close", time.Now()).Error)

	creditID := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		creditID, time.Now(), time.Now(), tenantID,
		"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", 10000, 3000, "open").Error)

	r := mountCloseCredit(db, tenantID)

	body := []byte(`{"force":true,"reason":"perdido"}`)
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/credits/"+creditID+"/close",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var row struct {
		Status   string
		ClosedAt *time.Time `gorm:"column:closed_at"`
	}
	require.NoError(t, db.Table("credit_accounts").
		Select("status, closed_at").
		Where("id = ?", creditID).
		Scan(&row).Error)
	assert.Equal(t, "paid", row.Status)
	require.NotNil(t, row.ClosedAt, "closed_at must be stamped on close")
	assert.WithinDuration(t, time.Now(), *row.ClosedAt, 5*time.Second,
		"closed_at must be ~now")
}

// TestRegisterCreditPayment_PersistsReceiptImageURL covers the
// Mandatory Image Receipts epic for fiado abonos: the URL the
// cashier sent must end up on the persisted CreditPayment row so the
// audit trail survives the 8-day Supabase TTL purge of the blob.
func TestRegisterCreditPayment_PersistsReceiptImageURL(t *testing.T) {
	db := setupCreditsDB(t)
	tenantID := "tenant-receipt-payment"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name, created_at) VALUES (?, ?, ?)`,
		tenantID, "Tienda Recibo", time.Now()).Error)

	creditID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		creditID, time.Now(), time.Now(), tenantID,
		"99999999-9999-9999-9999-999999999999", 10000, 0, "open").Error)

	r := mountCreditPayment(db, tenantID)

	receiptURL := "https://supabase.co/storage/v1/object/public/payment_receipts/abono.jpg"
	body := map[string]any{
		"amount":            5000,
		"payment_method":    "transfer",
		"note":              "abono digital",
		"receipt_image_url": receiptURL,
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/credits/"+creditID+"/payments",
		bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var stored struct {
		ReceiptImageURL string `gorm:"column:receipt_image_url"`
		PaymentMethod   string `gorm:"column:payment_method"`
	}
	require.NoError(t, db.Table("credit_payments").
		Select("receipt_image_url, payment_method").
		Where("credit_account_id = ?", creditID).
		Scan(&stored).Error)
	assert.Equal(t, receiptURL, stored.ReceiptImageURL,
		"the payment row must carry the receipt URL — audit trail")
	assert.Equal(t, "transfer", stored.PaymentMethod)
}

// TestRegisterCreditPayment_AllowsEmptyReceiptForCashAbono is the
// negative pair: cash abonos legitly omit the URL and must not 4xx.
func TestRegisterCreditPayment_AllowsEmptyReceiptForCashAbono(t *testing.T) {
	db := setupCreditsDB(t)
	tenantID := "tenant-cash-abono"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name, created_at) VALUES (?, ?, ?)`,
		tenantID, "Tienda Cash", time.Now()).Error)

	creditID := "abcdef01-2345-6789-abcd-ef0123456789"
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		creditID, time.Now(), time.Now(), tenantID,
		"11112222-3333-4444-5555-666677778888", 8000, 0, "open").Error)

	r := mountCreditPayment(db, tenantID)

	body := map[string]any{
		"amount":         3000,
		"payment_method": "cash",
		"note":           "abono efectivo",
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/credits/"+creditID+"/payments",
		bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var stored struct {
		ReceiptImageURL string `gorm:"column:receipt_image_url"`
	}
	require.NoError(t, db.Table("credit_payments").
		Select("receipt_image_url").
		Where("credit_account_id = ?", creditID).
		Scan(&stored).Error)
	assert.Equal(t, "", stored.ReceiptImageURL,
		"cash abonos persist with empty URL — informative, never enforced")
}

// ── CancelCredit: restaura stock + kardex con branch_id (2026-07-02) ───────
//
// Auditoría 2026-07-02 (concilio POS↔Inventario↔Kardex): CancelCredit
// restaura el stock de un fiado pendiente y registra un InventoryMovement,
// pero ese movimiento no propagaba branch_id (quedaba NULL) — a diferencia
// de la venta original, que sí lo lleva. En un tenant multi-sede esto
// sesga cualquier reporte de kardex filtrado por sucursal. Fix: usar
// sale.BranchID (la venta ya lo tiene) al loguear el movimiento.

// setupCreditsCancelDB extiende el esquema de setupCreditsDB con las tablas
// que CancelCredit SÍ toca (products, sales, sale_items) más
// InventoryMovement vía AutoMigrate (sin defaults Postgres-específicos que
// rompan sqlite, a diferencia de products/sales/sale_items).
func setupCreditsCancelDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupCreditsDB(t)
	stmts := []string{
		`CREATE TABLE products (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT,
			name TEXT NOT NULL, price REAL NOT NULL DEFAULT 0,
			stock INTEGER NOT NULL DEFAULT 0, is_available INTEGER DEFAULT 1
		)`,
		`CREATE TABLE sales (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT,
			credit_account_id TEXT, total REAL NOT NULL DEFAULT 0,
			payment_method TEXT NOT NULL DEFAULT 'credit'
		)`,
		`CREATE TABLE sale_items (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, sale_id TEXT NOT NULL, product_id TEXT,
			name TEXT NOT NULL, price REAL NOT NULL DEFAULT 0, quantity INTEGER NOT NULL,
			subtotal REAL NOT NULL DEFAULT 0, is_container_charge INTEGER DEFAULT 0,
			is_service INTEGER DEFAULT 0
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	require.NoError(t, db.AutoMigrate(&models.InventoryMovement{}))
	return db
}

func mountCancelCredit(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/api/v1/credits/:id/cancel", handlers.CancelCredit(db))
	return r
}

func TestCancelCredit_RestoresStockAndKardexKeepsBranchID(t *testing.T) {
	db := setupCreditsCancelDB(t)
	tenantID := "tenant-cancel-credit"
	branchID := "branch-norte"
	customerID := "cust-1"
	creditID := "credit-1"
	saleID := "sale-1"
	productID := "prod-gaseosa"

	require.NoError(t, db.Exec(
		`INSERT INTO customers (id, created_at, updated_at, tenant_id, name)
		 VALUES (?, ?, ?, ?, ?)`,
		customerID, time.Now(), time.Now(), tenantID, "Cliente Test").Error)
	require.NoError(t, db.Exec(
		`INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id, name, price, stock)
		 VALUES (?, ?, ?, ?, ?, ?, 2500, 18)`,
		productID, time.Now(), time.Now(), tenantID, branchID, "Gaseosa").Error)
	require.NoError(t, db.Exec(
		`INSERT INTO sales (id, created_at, updated_at, tenant_id, branch_id, credit_account_id, total, payment_method)
		 VALUES (?, ?, ?, ?, ?, ?, 5000, 'credit')`,
		saleID, time.Now(), time.Now(), tenantID, branchID, creditID).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO sale_items (id, created_at, updated_at, sale_id, product_id, name, price, quantity, subtotal)
		 VALUES (?, ?, ?, ?, ?, ?, 2500, 2, 5000)`,
		"item-1", time.Now(), time.Now(), saleID, productID, "Gaseosa").Error)
	require.NoError(t, db.Exec(
		`INSERT INTO credit_accounts (id, created_at, updated_at, tenant_id, branch_id, customer_id, sale_id, total_amount, paid_amount, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 5000, 0, 'pending')`,
		creditID, time.Now(), time.Now(), tenantID, branchID, customerID, saleID).Error)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/credits/"+creditID+"/cancel", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	mountCancelCredit(db, tenantID).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var p struct{ Stock int }
	require.NoError(t, db.Table("products").Select("stock").
		Where("id = ?", productID).Scan(&p).Error)
	assert.Equal(t, 20, p.Stock, "cancelar el fiado restaura las 2 gaseosas vendidas")

	var mov models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementSaleCancel).
		First(&mov).Error)
	require.NotNil(t, mov.BranchID,
		"el movimiento de cancelación de fiado debe llevar la sede de la venta")
	assert.Equal(t, branchID, *mov.BranchID)
}
