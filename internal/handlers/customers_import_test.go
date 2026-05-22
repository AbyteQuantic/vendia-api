// Spec: specs/026-importador-clientes/spec.md
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupImportDB creates an in-memory SQLite database with the customers table.
// The schema mirrors the structure used in other handler tests in this package
// (e.g. fiado_test.go, customer_consent_test.go).
func setupImportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Customer{}))
	return db
}

// importRouter builds a minimal Gin engine wired with the ImportCustomers
// handler. The tenantID is injected directly into the context (as the auth
// middleware would), and optionally a super-admin claim.
func importRouter(db *gorm.DB, tenantID string, isSuperAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/customers/import", func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		if isSuperAdmin {
			c.Set("claims", &mockSuperAdminClaims{IsSuperAdmin: isSuperAdmin, TenantID: tenantID})
		}
		ImportCustomers(db)(c)
	})
	return r
}

// mockSuperAdminClaims is a lightweight stand-in for auth.Claims used in
// god-mode tests. It satisfies the interface the handler reads via c.Get("claims").
type mockSuperAdminClaims struct {
	IsSuperAdmin bool
	TenantID     string
}

// postImport is a helper that POSTs JSON to /customers/import on the given router.
func postImport(r http.Handler, body any) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/customers/import", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// postImportWithHeader is like postImport but adds extra headers (for god-mode tests).
func postImportWithHeader(r http.Handler, body any, headers map[string]string) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/customers/import", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ── (a) Row sin name → failed con razón "nombre vacío" ──────────────────────

func TestImportCustomers_EmptyName_Failed(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "", "phone": "3001234567"}},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])
	assert.Equal(t, float64(0), data["updated"])

	failed := data["failed"].([]any)
	require.Len(t, failed, 1)
	f := failed[0].(map[string]any)
	assert.Equal(t, float64(0), f["row_index"])
	assert.Contains(t, strings.ToLower(f["reason"].(string)), "nombre")
}

// ── (b) Row con name de 1 char → failed con razón "nombre muy corto" ────────

func TestImportCustomers_ShortName_Failed(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "A"}},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])

	failed := data["failed"].([]any)
	require.Len(t, failed, 1)
	f := failed[0].(map[string]any)
	reason := strings.ToLower(f["reason"].(string))
	assert.True(t, strings.Contains(reason, "corto") || strings.Contains(reason, "mínimo") || strings.Contains(reason, "minimo"),
		"expected reason to mention short name, got: %s", f["reason"])
}

// ── (c) Row nuevo con teléfono → created, marketing_opt_in=false, terms_accepted=false ──

func TestImportCustomers_NewRowWithPhone_Created(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Juan Pérez", "phone": "3001234567", "email": "juan@example.com"},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])
	assert.Equal(t, float64(0), data["updated"])

	// Verify the customer was saved with Habeas Data invariants
	var customer models.Customer
	require.NoError(t, db.Where("phone = ? AND tenant_id = ?", "3001234567", "tenant-1").First(&customer).Error)
	assert.Equal(t, "Juan Pérez", customer.Name)
	assert.False(t, customer.MarketingOptIn, "marketing_opt_in MUST be false on import")
	assert.False(t, customer.TermsAccepted, "terms_accepted MUST be false on import")
}

// ── (d) Row con teléfono existente → updated, no toca campos protegidos ─────

func TestImportCustomers_ExistingPhone_Updated(t *testing.T) {
	db := setupImportDB(t)

	// Seed an existing customer with real business data that must not be overwritten
	existing := models.Customer{
		BaseModel:      models.BaseModel{ID: "cust-uuid-1"},
		TenantID:       "tenant-1",
		Name:           "Old Name",
		Phone:          "3001234567",
		Email:          "old@example.com",
		Notes:          "original notes",
		MarketingOptIn: true,  // Must NOT be overwritten by import
		TermsAccepted:  true,  // Must NOT be overwritten by import
	}
	require.NoError(t, db.Create(&existing).Error)

	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "New Name", "phone": "3001234567", "email": "new@example.com", "notes": "updated notes"},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])
	assert.Equal(t, float64(1), data["updated"])

	// Verify the customer was updated correctly
	var customer models.Customer
	require.NoError(t, db.Where("id = ?", "cust-uuid-1").First(&customer).Error)
	assert.Equal(t, "New Name", customer.Name)
	assert.Equal(t, "new@example.com", customer.Email)
	assert.Equal(t, "updated notes", customer.Notes)

	// Protected fields must NOT be touched
	assert.True(t, customer.MarketingOptIn, "marketing_opt_in must not be overwritten on update")
	assert.True(t, customer.TermsAccepted, "terms_accepted must not be overwritten on update")
	assert.Nil(t, customer.LastOrderAt, "last_order_at must not be touched")
}

// ── (e) Row sin teléfono → siempre INSERT, nunca dedup ───────────────────────

func TestImportCustomers_NoPhone_AlwaysCreated(t *testing.T) {
	db := setupImportDB(t)

	// Seed a customer with no phone to make sure a second phoneless row
	// does NOT get merged with the first
	require.NoError(t, db.Create(&models.Customer{
		BaseModel: models.BaseModel{ID: "cust-no-phone-1"},
		TenantID:  "tenant-1",
		Name:      "Existing No Phone",
		Phone:     "",
	}).Error)

	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "New No Phone Customer", "phone": ""},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])
	assert.Equal(t, float64(0), data["updated"])

	// Both phoneless customers should exist
	var count int64
	db.Model(&models.Customer{}).Where("tenant_id = ? AND (phone = '' OR phone IS NULL)", "tenant-1").Count(&count)
	assert.Equal(t, int64(2), count)
}

// ── (f) Payload con > 100 rows → 400 ────────────────────────────────────────

func TestImportCustomers_TooManyRows_400(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	rows := make([]map[string]any, 101)
	for i := range rows {
		rows[i] = map[string]any{"name": "Customer", "phone": ""}
	}

	payload := map[string]any{
		"rows":           rows,
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── (g) Payload sin rows → 400 ───────────────────────────────────────────────

func TestImportCustomers_MissingRows_400(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── (h) dedup_strategy distinto → 400 ───────────────────────────────────────

func TestImportCustomers_InvalidDedupStrategy_400(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "Juan"}},
		"dedup_strategy": "skip_duplicates",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestImportCustomers_EmptyDedupStrategy_400(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	// dedup_strategy missing entirely
	payload := map[string]any{
		"rows": []map[string]any{{"name": "Juan"}},
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── (i) Idempotencia: mismo lote dos veces → segunda vez 0 created ───────────

func TestImportCustomers_Idempotent(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Ana López", "phone": "3019876543"},
			{"name": "Pedro Gómez", "phone": "3109876543"},
		},
		"dedup_strategy": "merge_by_phone",
	}

	// First run
	w1 := postImport(r, payload)
	require.Equal(t, http.StatusOK, w1.Code)
	var resp1 map[string]any
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &resp1))
	data1 := resp1["data"].(map[string]any)
	assert.Equal(t, float64(2), data1["created"])
	assert.Equal(t, float64(0), data1["updated"])

	// Second run — same payload
	w2 := postImport(r, payload)
	require.Equal(t, http.StatusOK, w2.Code)
	var resp2 map[string]any
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	data2 := resp2["data"].(map[string]any)
	assert.Equal(t, float64(0), data2["created"], "second run must not duplicate")
	assert.Equal(t, float64(2), data2["updated"])
}

// ── (j) Habeas Data: aunque el payload diga marketing_opt_in=true, se guarda false ──

func TestImportCustomers_HabeaDataInvariant(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	// The client should not be able to force marketing_opt_in=true via the import
	payload := map[string]any{
		"rows": []map[string]any{
			{
				"name":             "Carlos Ley",
				"phone":            "3201234567",
				"marketing_opt_in": true, // attempt to override — must be ignored
			},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var customer models.Customer
	require.NoError(t, db.Where("phone = ? AND tenant_id = ?", "3201234567", "tenant-1").First(&customer).Error)
	assert.False(t, customer.MarketingOptIn, "marketing_opt_in MUST be false regardless of payload (Habeas Data, Ley 1581)")
	assert.False(t, customer.TermsAccepted, "terms_accepted MUST be false regardless of payload")
}

// ── Mixed batch: some valid, some invalid ────────────────────────────────────

func TestImportCustomers_MixedBatch(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Valid Customer", "phone": "3001111111"},
			{"name": ""},                                 // invalid — empty name
			{"name": "X"},                               // invalid — too short
			{"name": "Another Valid", "phone": "3002222222"},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(2), data["created"])
	assert.Equal(t, float64(0), data["updated"])
	assert.Equal(t, float64(2), float64(len(data["failed"].([]any))))
}

// ── God-mode: X-Tenant-Override without super_admin scope → 403 ─────────────

func TestImportCustomers_TenantOverride_NoSuperAdmin_403(t *testing.T) {
	db := setupImportDB(t)

	// Regular (non-super-admin) router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/customers/import", func(c *gin.Context) {
		c.Set("tenant_id", "tenant-regular")
		// No super-admin claims set
		ImportCustomers(db)(c)
	})

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "Test"}},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImportWithHeader(router, payload, map[string]string{
		"X-Tenant-Override": "other-tenant-uuid",
	})
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// ── God-mode: X-Tenant-Override with super_admin scope → uses override tenant ──

func TestImportCustomers_TenantOverride_SuperAdmin_OK(t *testing.T) {
	db := setupImportDB(t)

	targetTenantID := "target-tenant-uuid"

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/customers/import", func(c *gin.Context) {
		c.Set("tenant_id", "super-admin-tenant")
		// Simulate super-admin claims
		c.Set("is_super_admin", true)
		ImportCustomers(db)(c)
	})

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "God Mode Customer", "phone": "3001112233"}},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImportWithHeader(router, payload, map[string]string{
		"X-Tenant-Override": targetTenantID,
	})
	assert.Equal(t, http.StatusOK, w.Code)

	// The customer must have been created under the override tenant
	var customer models.Customer
	require.NoError(t, db.Where("phone = ? AND tenant_id = ?", "3001112233", targetTenantID).First(&customer).Error)
	assert.Equal(t, "God Mode Customer", customer.Name)
}

// ── Whitespace sanitization: name with extra spaces is trimmed ────────────────

func TestImportCustomers_WhitespaceSanitization(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "  Juan   Pérez  ", "phone": "  +57 300 111 2222  "},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])

	// Name should be trimmed; phone should be normalized
	var customer models.Customer
	require.NoError(t, db.Where("tenant_id = ?", "tenant-1").First(&customer).Error)
	// normalizeWhitespace collapses all internal runs of whitespace to a single space
	assert.Equal(t, "Juan Pérez", customer.Name)
	assert.Equal(t, "573001112222", customer.Phone)
}

// ── Phone normalization affects dedup: "300-123" matches "300123" ────────────

func TestImportCustomers_PhoneNormalizationDedup(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	// First insert: plain digits
	payload1 := map[string]any{
		"rows":           []map[string]any{{"name": "First Insert", "phone": "3001234567"}},
		"dedup_strategy": "merge_by_phone",
	}
	w1 := postImport(r, payload1)
	require.Equal(t, http.StatusOK, w1.Code)

	// Second insert: formatted phone that normalizes to same digits
	payload2 := map[string]any{
		"rows":           []map[string]any{{"name": "Should Update", "phone": "(300) 123-4567"}},
		"dedup_strategy": "merge_by_phone",
	}
	w2 := postImport(r, payload2)
	require.Equal(t, http.StatusOK, w2.Code)

	var resp2 map[string]any
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	data2 := resp2["data"].(map[string]any)
	assert.Equal(t, float64(0), data2["created"])
	assert.Equal(t, float64(1), data2["updated"])
}

// ── (F032 AC-03) Email format validation in the importer ────────────────────

// A row carrying a malformed email is reported as failed, like any other
// validation error, and is NOT persisted.
func TestImportCustomers_InvalidEmail_Failed(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Cliente Malo", "phone": "3009998877", "email": "no-es-un-email"},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])

	failed := data["failed"].([]any)
	require.Len(t, failed, 1)
	f := failed[0].(map[string]any)
	assert.Contains(t, strings.ToLower(f["reason"].(string)), "email")

	// Nothing persisted.
	var count int64
	db.Model(&models.Customer{}).Count(&count)
	assert.Equal(t, int64(0), count)
}

// An empty email is valid — the field is optional (AC-07). The row is created.
func TestImportCustomers_EmptyEmail_Created(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Sin Correo", "phone": "3001112222", "email": ""},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])
	assert.Empty(t, data["failed"])
}

// A well-formed email is accepted and stored on the customer record.
func TestImportCustomers_ValidEmail_Created(t *testing.T) {
	db := setupImportDB(t)
	r := importRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Cliente Bueno", "phone": "3003334444", "email": "cliente@correo.com"},
		},
		"dedup_strategy": "merge_by_phone",
	}

	w := postImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])

	var customer models.Customer
	require.NoError(t, db.Where("phone = ?", "3003334444").First(&customer).Error)
	assert.Equal(t, "cliente@correo.com", customer.Email)
}

// isValidEmail unit coverage — accepts valid forms, rejects malformed ones.
func TestIsValidEmail(t *testing.T) {
	valid := []string{
		"a@b.co",
		"don.pedro@gmail.com",
		"cliente+ventas@ferreteria.com.co",
	}
	for _, e := range valid {
		assert.True(t, isValidEmail(e), "expected %q to be valid", e)
	}

	invalid := []string{
		"abc",
		"abc@",
		"@xyz.com",
		"abc@xyz",
		"abc xyz@mail.com",
		"Nombre <a@b.co>",
	}
	for _, e := range invalid {
		assert.False(t, isValidEmail(e), "expected %q to be invalid", e)
	}
}
