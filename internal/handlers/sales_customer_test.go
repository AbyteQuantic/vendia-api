// Spec: specs/030-administracion-clientes-no-tienda/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ── F030 — CreateSale customer_id association tests ─────────────────────────
//
// CreateSale already accepted an optional customer_id; F030 adds tenant
// ownership validation. These tests exercise the three branches: valid
// owned customer → persisted link, foreign customer → rejected, absent
// customer_id → anonymous sale still succeeds.

// setupSalesCustomerDB extends the branch-isolation schema with a customers
// table so CreateSale's F030 ownership lookup has something to query.
func setupSalesCustomerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupIsolationDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE customers (
		id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
		deleted_at DATETIME, tenant_id TEXT NOT NULL,
		name TEXT NOT NULL, phone TEXT DEFAULT '',
		email TEXT DEFAULT '', notes TEXT DEFAULT '',
		marketing_opt_in INTEGER DEFAULT 0,
		terms_accepted INTEGER DEFAULT 0,
		terms_accepted_at DATETIME, last_order_at DATETIME
	)`).Error)
	return db
}

// seedCustomerRow inserts a customer for a tenant and returns its id.
func seedCustomerRow(t *testing.T, db *gorm.DB, id, tenantID, name, phone string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO customers (id, created_at, updated_at, tenant_id, name, phone)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, time.Now(), time.Now(), tenantID, name, phone).Error)
}

func postSale(r http.Handler, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestCreateSale_WithValidCustomer verifies a sale carrying a customer_id
// owned by the tenant persists the link and snapshots name/phone
// (F030 AC-04 / T-10).
func TestCreateSale_WithValidCustomer(t *testing.T) {
	db := setupSalesCustomerDB(t)

	tenantID := "tenant-cust-ok"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "a1111111-1111-1111-1111-111111111111"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")
	productID := "b1111111-1111-1111-1111-111111111111"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Pan", 50, 1500)

	customerID := "c1111111-1111-1111-1111-111111111111"
	seedCustomerRow(t, db, customerID, tenantID, "María Pérez", "3001112233")

	r := mountSalesHandler(db, tenantID, branchID)
	w := postSale(r, map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"customer_id":    customerID,
		"items":          []map[string]any{{"product_id": productID, "quantity": 2}},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.CustomerID, "la venta debe quedar ligada al cliente")
	assert.Equal(t, customerID, *resp.Data.CustomerID)
	assert.Equal(t, "María Pérez", resp.Data.CustomerNameSnapshot,
		"el nombre del cliente debe congelarse en la venta")
	assert.Equal(t, "3001112233", resp.Data.CustomerPhoneSnapshot)

	// Confirm it persisted.
	var stored models.Sale
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&stored).Error)
	require.NotNil(t, stored.CustomerID)
	assert.Equal(t, customerID, *stored.CustomerID)
}

// TestCreateSale_WithForeignCustomer verifies a customer_id belonging to a
// different tenant is rejected with 404 — never silently attached
// (F030 / T-10, Constitución Art. III).
func TestCreateSale_WithForeignCustomer(t *testing.T) {
	db := setupSalesCustomerDB(t)

	tenantID := "tenant-cust-self"
	foreignTenant := "tenant-cust-other"
	for _, tid := range []string{tenantID, foreignTenant} {
		require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
			tid, time.Now()).Error)
	}

	branchID := "a2222222-2222-2222-2222-222222222222"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")
	productID := "b2222222-2222-2222-2222-222222222222"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Leche", 50, 3000)

	// Customer belongs to the OTHER tenant.
	foreignCustomer := "c2222222-2222-2222-2222-222222222222"
	seedCustomerRow(t, db, foreignCustomer, foreignTenant, "Cliente Ajeno", "3009998877")

	r := mountSalesHandler(db, tenantID, branchID)
	w := postSale(r, map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"customer_id":    foreignCustomer,
		"items":          []map[string]any{{"product_id": productID, "quantity": 1}},
	})
	assert.Equal(t, http.StatusNotFound, w.Code,
		"un customer_id de otro tenant debe ser rechazado")

	// No sale should have been created.
	var count int64
	require.NoError(t, db.Model(&models.Sale{}).
		Where("tenant_id = ?", tenantID).Count(&count).Error)
	assert.EqualValues(t, 0, count, "no debe persistirse ninguna venta")
}

// TestCreateSale_AnonymousSale verifies a sale WITHOUT customer_id still
// succeeds and leaves customer_id null (F030 AC-04 — asociación opcional).
func TestCreateSale_AnonymousSale(t *testing.T) {
	db := setupSalesCustomerDB(t)

	tenantID := "tenant-cust-anon"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "a3333333-3333-3333-3333-333333333333"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")
	productID := "b3333333-3333-3333-3333-333333333333"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Café", 50, 2000)

	r := mountSalesHandler(db, tenantID, branchID)
	w := postSale(r, map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items":          []map[string]any{{"product_id": productID, "quantity": 1}},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Nil(t, resp.Data.CustomerID,
		"una venta sin customer_id debe quedar anónima (customer_id null)")
}

// TestCreateSale_EmptyCustomerID verifies an explicit empty-string
// customer_id is normalised to NULL — not persisted as an empty string that
// would break the Postgres uuid column (feedback_nullable_uuid_rule).
func TestCreateSale_EmptyCustomerID(t *testing.T) {
	db := setupSalesCustomerDB(t)

	tenantID := "tenant-cust-empty"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "a4444444-4444-4444-4444-444444444444"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")
	productID := "b4444444-4444-4444-4444-444444444444"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Arroz", 50, 4000)

	r := mountSalesHandler(db, tenantID, branchID)
	w := postSale(r, map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"customer_id":    "",
		"items":          []map[string]any{{"product_id": productID, "quantity": 1}},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Nil(t, resp.Data.CustomerID,
		"customer_id vacío debe quedar como NULL, no como cadena vacía")
}

// TestCreateSale_InvalidCustomerID verifies a malformed customer_id is
// rejected with 400 before any DB write (F030 / T-10).
func TestCreateSale_InvalidCustomerID(t *testing.T) {
	db := setupSalesCustomerDB(t)

	tenantID := "tenant-cust-bad"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "a5555555-5555-5555-5555-555555555555"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")
	productID := "b5555555-5555-5555-5555-555555555555"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Azúcar", 50, 3500)

	r := mountSalesHandler(db, tenantID, branchID)
	w := postSale(r, map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"customer_id":    "no-soy-un-uuid",
		"items":          []map[string]any{{"product_id": productID, "quantity": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"un customer_id que no es UUID debe devolver 400")
}
