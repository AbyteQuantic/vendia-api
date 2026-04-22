package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

// ── Helpers ───────────────────────────────────────────────────────────────────
//
// These tests rely on setupTestDB (defined in tenant_register_test.go) which
// skips when the Docker Postgres isn't running, so this suite is always
// safe to `go test ./...` on a fresh clone.

// injectTenantID returns a middleware that fakes the Auth context so
// the handler's middleware.GetTenantID(c) call returns the value we
// need without wiring the full JWT middleware.
func injectTenantID(id string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, id)
		c.Next()
	}
}

// createTenantWithBusinessName inserts a minimal Tenant row with the
// business name we care about for the test and returns its UUID. The
// other fields are filled with safe defaults — the test only exercises
// the slug columns.
func createTenantWithBusinessName(t *testing.T, db *gorm.DB, name, phone string) string {
	t.Helper()
	tenant := models.Tenant{
		OwnerName:    "Tester",
		Phone:        phone,
		PasswordHash: "x",
		BusinessName: name,
		SaleTypes:    []string{"tienda"},
	}
	require.NoError(t, db.Create(&tenant).Error)
	return tenant.ID
}

func cleanupTenant(t *testing.T, db *gorm.DB, tenantID string) {
	t.Helper()
	db.Unscoped().Where("id = ?", tenantID).Delete(&models.Tenant{})
}

func setupSlugRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectTenantID(tenantID))
	r.GET("/api/v1/store/slug", handlers.GetStoreSlug(db))
	r.PATCH("/api/v1/store/slug", handlers.UpdateStoreSlug(db))
	return r
}

// ── GET ───────────────────────────────────────────────────────────────────────

func TestGetStoreSlug_AutoGeneratesFromBusinessName(t *testing.T) {
	db := setupTestDB(t)
	tenantID := createTenantWithBusinessName(t, db, "Tienda Don Pepe", uniquePhone())
	t.Cleanup(func() { cleanupTenant(t, db, tenantID) })

	r := setupSlugRouter(db, tenantID)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/store/slug", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var resp struct {
		Data struct {
			Slug      string `json:"slug"`
			BaseURL   string `json:"base_url"`
			PublicURL string `json:"public_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Slug should start with the sanitized business name and end
	// with a short hex suffix — this is the ticket's canonical shape.
	assert.Regexp(t, `^tienda-don-pepe-[0-9a-f]{4,8}$`, resp.Data.Slug)
	assert.NotEmpty(t, resp.Data.BaseURL)
	assert.Equal(t, resp.Data.BaseURL+"/"+resp.Data.Slug, resp.Data.PublicURL)

	// Second GET must return the SAME slug (it was persisted, not
	// regenerated — otherwise shared links rot).
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("GET", "/api/v1/store/slug", nil)
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code)
	var resp2 struct{ Data struct{ Slug string } }
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	assert.Equal(t, resp.Data.Slug, resp2.Data.Slug,
		"slug must be stable across reads")
}

// ── PATCH ─────────────────────────────────────────────────────────────────────

func TestUpdateStoreSlug_Conflict(t *testing.T) {
	db := setupTestDB(t)

	// Tenant A reserves a slug; tenant B tries to take it and must
	// receive a 409 with a user-friendly Spanish message.
	tenantA := createTenantWithBusinessName(t, db, "Tienda A", uniquePhone())
	tenantB := createTenantWithBusinessName(t, db, "Tienda B", uniquePhone())
	t.Cleanup(func() {
		cleanupTenant(t, db, tenantA)
		cleanupTenant(t, db, tenantB)
	})

	// Prime tenant A with a known slug via direct write (bypassing
	// the handler keeps this test focused on the conflict path).
	reservedSlug := "slug-reservado-" + uniquePhone()[len(uniquePhone())-4:]
	require.NoError(t, db.Model(&models.Tenant{}).
		Where("id = ?", tenantA).
		Update("store_slug", reservedSlug).Error)

	r := setupSlugRouter(db, tenantB)

	body, _ := json.Marshal(map[string]string{"slug": reservedSlug})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/v1/store/slug", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "en uso")
}

func TestUpdateStoreSlug_InvalidFormat(t *testing.T) {
	db := setupTestDB(t)
	tenantID := createTenantWithBusinessName(t, db, "Tienda X", uniquePhone())
	t.Cleanup(func() { cleanupTenant(t, db, tenantID) })

	r := setupSlugRouter(db, tenantID)

	cases := []struct {
		name string
		slug string
	}{
		{"uppercase", "MiTienda"},
		{"spaces", "mi tienda"},
		{"leading-dash", "-mitienda"},
		{"too-short", "ab"},
		{"underscore", "mi_tienda"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"slug": tc.slug})
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PATCH", "/api/v1/store/slug", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code,
				"case %s: body=%s", tc.name, w.Body.String())
		})
	}
}

func TestUpdateStoreSlug_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	tenantID := createTenantWithBusinessName(t, db, "Tienda X", uniquePhone())
	t.Cleanup(func() { cleanupTenant(t, db, tenantID) })

	r := setupSlugRouter(db, tenantID)

	// Use a clearly-unique slug so this test is parallel-safe.
	slug := "mi-super-tienda-" + uniquePhone()[len(uniquePhone())-4:]

	body, _ := json.Marshal(map[string]string{"slug": slug})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/v1/store/slug", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var resp struct {
		Data struct {
			Slug      string `json:"slug"`
			PublicURL string `json:"public_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, slug, resp.Data.Slug)
	assert.Contains(t, resp.Data.PublicURL, slug)
}
