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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupFiadoDB hand-crafts the SQLite schema for the fiado handlers.
// CreditAccount carries Postgres-specific defaults that AutoMigrate
// can't translate cleanly, so we DDL the bare minimum the handler
// touches. The same pattern is used in branch_isolation_test.go
// — keeps tests fast, deterministic, and free of fixture drift.
func setupFiadoDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	stmts := []string{
		`CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '', phone TEXT DEFAULT '',
			logo_url TEXT DEFAULT '',
			payment_method_name TEXT DEFAULT '',
			payment_account_number TEXT DEFAULT '',
			payment_account_holder TEXT DEFAULT '',
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE customers (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '', phone TEXT DEFAULT '',
			email TEXT DEFAULT '', notes TEXT DEFAULT '',
			marketing_opt_in INTEGER DEFAULT 0,
			terms_accepted INTEGER DEFAULT 0,
			terms_accepted_at DATETIME,
			last_order_at DATETIME
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
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

func mountInitFiado(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/api/v1/fiado/init", InitFiado(db))
	return r
}

// TestInitFiado_NormalizesPhone verifies the customer is keyed by the
// normalized form (digits only) so the same human entered with three
// different decorations resolves to ONE customer row.
func TestInitFiado_NormalizesPhone(t *testing.T) {
	db := setupFiadoDB(t)
	tenantID := "tenant-norm"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name) VALUES (?, ?)`,
		tenantID, "Tienda Norm").Error)

	r := mountInitFiado(db, tenantID)

	// First call seeds a customer row with the canonical phone.
	body := map[string]any{
		"customer_name":  "Viviana",
		"customer_phone": "(300) 123-4567",
		"total_amount":   10000,
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/fiado/init", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var customers []models.Customer
	require.NoError(t, db.Where("tenant_id = ?", tenantID).Find(&customers).Error)
	require.Len(t, customers, 1, "exactly one customer row should be created")
	assert.Equal(t, "3001234567", customers[0].Phone,
		"phone must be stored in canonical digit-only form")

	// Second call uses a totally different cosmetic spelling of the same
	// phone — must hit the existing customer (no duplicate row) and now
	// the one-open-account guard surfaces needs_confirmation instead of
	// minting a second pending fiado.
	body2 := map[string]any{
		"customer_name":  "Viviana",
		"customer_phone": "+57 300-123-4567",
		"total_amount":   5000,
	}
	raw2, _ := json.Marshal(body2)
	req2, _ := http.NewRequest(http.MethodPost, "/api/v1/fiado/init", bytes.NewReader(raw2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code, w2.Body.String())
	assert.Contains(t, w2.Body.String(), "needs_confirmation",
		"second call with same canonical phone must hit the one-open-account gate")

	// Customer table still has exactly one row.
	require.NoError(t, db.Where("tenant_id = ?", tenantID).Find(&customers).Error)
	assert.Len(t, customers, 1, "no duplicate customer should be created from cosmetically-different phone")
}

// TestInitFiado_BlocksDuplicateOnPartial proves the rule expansion: a
// customer whose only open account is in 'partial' state (mid-abono)
// MUST still trigger needs_confirmation. Before the epic this only
// fired on status='open'.
func TestInitFiado_BlocksDuplicateOnPartial(t *testing.T) {
	db := setupFiadoDB(t)
	tenantID := "tenant-partial"
	customerID := "11111111-1111-1111-1111-111111111111"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name) VALUES (?, ?)`,
		tenantID, "Tienda Partial").Error)
	require.NoError(t, db.Exec(`
		INSERT INTO customers (id, created_at, updated_at, tenant_id, name, phone)
		VALUES (?, datetime('now'), datetime('now'), ?, ?, ?)`,
		customerID, tenantID, "Marta", "3009998888").Error)
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status, fiado_status)
		VALUES (?, datetime('now'), datetime('now'), ?, ?, ?, ?, ?, ?)`,
		"22222222-2222-2222-2222-222222222222", tenantID, customerID,
		20000, 8000, "partial", "accepted").Error)

	r := mountInitFiado(db, tenantID)

	body := map[string]any{
		"customer_name":  "Marta",
		"customer_phone": "3009998888",
		"total_amount":   5000,
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/fiado/init", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "needs_confirmation",
		"a partial-status account must block silent creation of a second one")
	assert.Contains(t, w.Body.String(), "22222222-2222-2222-2222-222222222222",
		"response must echo the existing credit_id so the cashier can confirm")
}

// TestInitFiado_BlocksDuplicateOnPending mirrors the partial case for
// status='pending' (link sent, customer hasn't accepted yet). Without
// this check a cashier could spam-init while a previous handshake was
// still pending and end up with two parallel ledger accounts.
func TestInitFiado_BlocksDuplicateOnPending(t *testing.T) {
	db := setupFiadoDB(t)
	tenantID := "tenant-pending"
	customerID := "33333333-3333-3333-3333-333333333333"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, business_name) VALUES (?, ?)`,
		tenantID, "Tienda Pending").Error)
	require.NoError(t, db.Exec(`
		INSERT INTO customers (id, created_at, updated_at, tenant_id, name, phone)
		VALUES (?, datetime('now'), datetime('now'), ?, ?, ?)`,
		customerID, tenantID, "Pedro", "3001112222").Error)
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status, fiado_status, fiado_token)
		VALUES (?, datetime('now'), datetime('now'), ?, ?, ?, ?, ?, ?, ?)`,
		"44444444-4444-4444-4444-444444444444", tenantID, customerID,
		15000, 0, "pending", "link_sent", "tok-pending-1").Error)

	r := mountInitFiado(db, tenantID)

	body := map[string]any{
		"customer_name":  "Pedro",
		"customer_phone": "3001112222",
		"total_amount":   8000,
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/fiado/init", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "needs_confirmation",
		"a pending-status account must block silent creation of a second one")
	assert.Contains(t, w.Body.String(), "44444444-4444-4444-4444-444444444444",
		"response must echo the existing pending credit_id")

	// And we must NOT have created a second credit_accounts row.
	var count int64
	require.NoError(t, db.Table("credit_accounts").
		Where("tenant_id = ? AND customer_id = ?", tenantID, customerID).
		Count(&count).Error)
	assert.EqualValues(t, 1, count, "no duplicate credit_account should be created")
}
