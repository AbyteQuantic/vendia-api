package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupConsentTestDB is purposely local to the consent tests so a
// future change to the shared store helper can't accidentally break
// our Habeas-Data invariants. AutoMigrate here includes Customer —
// without it the lookup would always return "not found" and every
// test would pass spuriously.
func setupConsentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Only the tables touched by CheckCustomerConsent and
	// upsertCustomerFromOrder. OnlineOrder uses a Postgres-specific
	// `gen_random_uuid()` default that SQLite can't parse, and we
	// don't exercise it here — the upsert helper is driven directly.
	if err := db.AutoMigrate(&models.Tenant{}, &models.Customer{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedTenant(t *testing.T, db *gorm.DB, id, slug string) models.Tenant {
	t.Helper()
	s := slug
	// Phone is UNIQUE on the tenants table, so two seeded tenants in
	// the same test (cross-tenant isolation) must get distinct
	// placeholder phones. We derive one from the id.
	tenant := models.Tenant{
		BaseModel:    models.BaseModel{ID: id},
		BusinessName: "Store " + slug,
		Phone:        "seed-" + id,
		StoreSlug:    &s,
	}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return tenant
}

func postJSON(r http.Handler, path string, body any) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Table-driven truth table for the check-customer endpoint. Covers
// the three business branches from the brief plus the edge cases we
// must NOT regress on (empty body, bad phone shape).
func TestCheckCustomerConsent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type setup struct {
		name          string
		phoneQuery    string
		bodyOverride  []byte // if set, skip marshalling and send this raw
		seedPhone     string
		seedAccepted  bool
		expectConsent bool
	}
	cases := []setup{
		{
			name:          "row missing → needs consent",
			phoneQuery:    "3001234567",
			expectConsent: true,
		},
		{
			name:          "row exists but not accepted → needs consent",
			phoneQuery:    "3001234567",
			seedPhone:     "3001234567",
			seedAccepted:  false,
			expectConsent: true,
		},
		{
			name:          "row exists and accepted → no consent",
			phoneQuery:    "3001234567",
			seedPhone:     "3001234567",
			seedAccepted:  true,
			expectConsent: false,
		},
		{
			name:          "phone with country code +57 matches bare number",
			phoneQuery:    "+57 300 123 4567",
			seedPhone:     "3001234567",
			seedAccepted:  true,
			expectConsent: false,
		},
		{
			name:          "empty phone fails closed",
			phoneQuery:    "",
			expectConsent: true,
		},
		{
			name:          "malformed body fails closed",
			bodyOverride:  []byte(`{not json`),
			expectConsent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupConsentTestDB(t)
			tenant := seedTenant(t, db, "tenant-1", "tienda")
			if tc.seedPhone != "" {
				now := time.Now().UTC()
				row := models.Customer{
					TenantID:      tenant.ID,
					Name:          "Alicia",
					Phone:         tc.seedPhone,
					TermsAccepted: tc.seedAccepted,
				}
				if tc.seedAccepted {
					row.TermsAcceptedAt = &now
				}
				if err := db.Create(&row).Error; err != nil {
					t.Fatalf("seed customer: %v", err)
				}
			}

			r := gin.New()
			r.POST("/api/v1/public/catalog/:slug/check-customer", CheckCustomerConsent(db))

			var w *httptest.ResponseRecorder
			if tc.bodyOverride != nil {
				req, _ := http.NewRequest(http.MethodPost,
					"/api/v1/public/catalog/tienda/check-customer",
					bytes.NewReader(tc.bodyOverride))
				req.Header.Set("Content-Type", "application/json")
				w = httptest.NewRecorder()
				r.ServeHTTP(w, req)
			} else {
				w = postJSON(r, "/api/v1/public/catalog/tienda/check-customer",
					map[string]string{"phone": tc.phoneQuery})
			}

			assert.Equal(t, http.StatusOK, w.Code)

			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			assert.Equal(t, tc.expectConsent, resp["needs_consent"],
				"needs_consent mismatch for case %q — body: %s", tc.name, w.Body.String())

			// P0 security guarantee: the response body MUST NOT ever
			// contain the customer's name, email, or order count.
			body := w.Body.String()
			for _, forbidden := range []string{"Alicia", "@", "last_order"} {
				assert.False(t, strings.Contains(body, forbidden),
					"check-customer leaked PII %q in response: %s", forbidden, body)
			}
		})
	}
}

func TestCheckCustomerConsent_UnknownSlugReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupConsentTestDB(t)

	r := gin.New()
	r.POST("/api/v1/public/catalog/:slug/check-customer", CheckCustomerConsent(db))

	w := postJSON(r, "/api/v1/public/catalog/does-not-exist/check-customer",
		map[string]string{"phone": "3001234567"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Cross-tenant isolation: if tenant A knows customer P, tenant B
// MUST still be told to ask for consent on the same phone. Otherwise
// we'd leak "this phone is a customer of vendia" across tenants.
func TestCheckCustomerConsent_CrossTenantIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupConsentTestDB(t)

	tenantA := seedTenant(t, db, "tenant-a", "shop-a")
	_ = seedTenant(t, db, "tenant-b", "shop-b")

	now := time.Now().UTC()
	db.Create(&models.Customer{
		TenantID:        tenantA.ID,
		Name:            "Alicia",
		Phone:           "3001234567",
		TermsAccepted:   true,
		TermsAcceptedAt: &now,
	})

	r := gin.New()
	r.POST("/api/v1/public/catalog/:slug/check-customer", CheckCustomerConsent(db))

	// Tenant A: already consented → no re-prompt.
	wA := postJSON(r, "/api/v1/public/catalog/shop-a/check-customer",
		map[string]string{"phone": "3001234567"})
	assert.Equal(t, http.StatusOK, wA.Code)
	var rA map[string]any
	_ = json.Unmarshal(wA.Body.Bytes(), &rA)
	assert.Equal(t, false, rA["needs_consent"])

	// Tenant B: same phone, but this tenant has no relationship yet
	// → needs_consent must be true.
	wB := postJSON(r, "/api/v1/public/catalog/shop-b/check-customer",
		map[string]string{"phone": "3001234567"})
	assert.Equal(t, http.StatusOK, wB.Code)
	var rB map[string]any
	_ = json.Unmarshal(wB.Body.Bytes(), &rB)
	assert.Equal(t, true, rB["needs_consent"])
}

// upsertCustomerFromOrder contract tests. These run the helper
// directly (no HTTP) so the assertions focus purely on the CRM
// semantics — less brittle than driving through the order handler.
func TestUpsertCustomerFromOrder_InsertsNewCustomerWithConsent(t *testing.T) {
	db := setupConsentTestDB(t)
	tenant := seedTenant(t, db, "tenant-1", "shop")

	created, err := upsertCustomerFromOrder(db, tenant.ID, "Juan Pérez", "3001234567", true)
	assert.NoError(t, err)
	assert.True(t, created, "first call should create the row")

	var row models.Customer
	err = db.Where("tenant_id = ? AND phone = ?", tenant.ID, "3001234567").First(&row).Error
	assert.NoError(t, err)
	assert.Equal(t, "Juan Pérez", row.Name)
	assert.True(t, row.TermsAccepted)
	if assert.NotNil(t, row.TermsAcceptedAt) {
		assert.WithinDuration(t, time.Now().UTC(), *row.TermsAcceptedAt, 5*time.Second)
	}
	if assert.NotNil(t, row.LastOrderAt) {
		assert.WithinDuration(t, time.Now().UTC(), *row.LastOrderAt, 5*time.Second)
	}
}

func TestUpsertCustomerFromOrder_UpdatesExistingAndFlipsConsentForward(t *testing.T) {
	db := setupConsentTestDB(t)
	tenant := seedTenant(t, db, "tenant-1", "shop")

	earlier := time.Now().UTC().Add(-30 * 24 * time.Hour)
	db.Create(&models.Customer{
		TenantID:      tenant.ID,
		Name:          "Juan",
		Phone:         "3001234567",
		TermsAccepted: false,
		LastOrderAt:   &earlier,
	})

	_, err := upsertCustomerFromOrder(db, tenant.ID, "Juan Pérez", "3001234567", true)
	assert.NoError(t, err)

	var row models.Customer
	db.Where("tenant_id = ? AND phone = ?", tenant.ID, "3001234567").First(&row)
	assert.Equal(t, "Juan Pérez", row.Name, "name should be updated")
	assert.True(t, row.TermsAccepted, "consent must flip from false to true")
	assert.NotNil(t, row.TermsAcceptedAt, "timestamp must be set on the flip")
	assert.NotNil(t, row.LastOrderAt)
	assert.True(t, row.LastOrderAt.After(earlier), "last_order_at must advance")
}

// Idempotence: calling twice with the same payload must not create
// duplicates and must not drop consent. This is the guard against
// a client retrying the POST /orders and wiping earlier consent.
func TestUpsertCustomerFromOrder_DoesNotRevokeConsentSilently(t *testing.T) {
	db := setupConsentTestDB(t)
	tenant := seedTenant(t, db, "tenant-1", "shop")

	// Prior consent granted in a previous visit.
	prior := time.Now().UTC().Add(-24 * time.Hour)
	db.Create(&models.Customer{
		TenantID:        tenant.ID,
		Name:            "Ana",
		Phone:           "3001112222",
		TermsAccepted:   true,
		TermsAcceptedAt: &prior,
	})

	// New order comes in with accepted_terms=false (legacy client
	// that didn't send the field). Consent must stay TRUE.
	_, err := upsertCustomerFromOrder(db, tenant.ID, "Ana", "3001112222", false)
	assert.NoError(t, err)

	var row models.Customer
	db.Where("tenant_id = ? AND phone = ?", tenant.ID, "3001112222").First(&row)
	assert.True(t, row.TermsAccepted, "never revoke consent from a missing field")
	assert.NotNil(t, row.TermsAcceptedAt)

	// Also make sure we didn't duplicate the row.
	var count int64
	db.Model(&models.Customer{}).
		Where("tenant_id = ? AND phone = ?", tenant.ID, "3001112222").
		Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestUpsertCustomerFromOrder_SkipsWhenPhoneMissing(t *testing.T) {
	db := setupConsentTestDB(t)
	tenant := seedTenant(t, db, "tenant-1", "shop")

	created, err := upsertCustomerFromOrder(db, tenant.ID, "Anon", "", true)
	assert.NoError(t, err)
	assert.False(t, created)

	var count int64
	db.Model(&models.Customer{}).Where("tenant_id = ?", tenant.ID).Count(&count)
	assert.Equal(t, int64(0), count, "anonymous order must NOT create a customer row")
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"3001234567":       "3001234567",
		"+57 300 123 4567": "3001234567",
		"(300) 123-4567":   "3001234567",
		"573001234567":     "3001234567",
		"":                 "",
		"abc":              "",
		// Short codes (e.g. USSD) stay intact.
		"#345#":  "345",
		"  123 ": "123",
	}
	for input, expected := range cases {
		assert.Equal(t, expected, normalizePhone(input), "normalizePhone(%q)", input)
	}
}
