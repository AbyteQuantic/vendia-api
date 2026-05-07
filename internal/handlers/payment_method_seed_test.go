package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/database"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

// ── seed-only helpers ─────────────────────────────────────────────────────────
//
// These tests intentionally do NOT spin up the full Tenant model on
// sqlite — its `type:jsonb` columns and `serializer:json` slices don't
// translate. We only need a `tenants(id, deleted_at)` skeleton plus
// the real payment_methods table to exercise SeedDefaultPaymentMethods
// and the ?active=true filter.

func setupSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Minimal tenants table — enough for the LEFT JOIN to find rows.
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`).Error)

	// Real payment_methods schema via the GORM model.
	require.NoError(t, db.AutoMigrate(&models.TenantPaymentMethod{}))
	return db
}

func insertTenant(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, created_at, updated_at) VALUES (?, ?, ?)`,
		id, now, now,
	).Error)
}

// ── Case 1: backfill seeds Efectivo for a tenant with zero methods ───────────

func TestSeedDefaultPaymentMethods_InsertsForOrphanTenant(t *testing.T) {
	db := setupSeedTestDB(t)

	tenantID := uuid.NewString()
	insertTenant(t, db, tenantID)

	require.NoError(t, database.SeedDefaultPaymentMethods(db))

	var methods []models.TenantPaymentMethod
	require.NoError(t,
		db.Where("tenant_id = ?", tenantID).Find(&methods).Error)
	require.Len(t, methods, 1, "expected exactly one seeded method")

	got := methods[0]
	assert.Equal(t, "Efectivo", got.Name)
	assert.Equal(t, "cash", got.Provider)
	assert.True(t, got.IsActive)
	assert.NotEmpty(t, got.ID, "BaseModel.BeforeCreate must assign UUID")
}

// ── Case 2: idempotency — re-running does NOT add a second row ───────────────

func TestSeedDefaultPaymentMethods_IsIdempotent(t *testing.T) {
	db := setupSeedTestDB(t)

	tenantID := uuid.NewString()
	insertTenant(t, db, tenantID)

	// Tenant already has a method — backfill must skip.
	require.NoError(t, db.Create(&models.TenantPaymentMethod{
		TenantID: tenantID,
		Name:     "Nequi",
		Provider: "nequi",
		IsActive: true,
	}).Error)

	require.NoError(t, database.SeedDefaultPaymentMethods(db))

	var count int64
	require.NoError(t,
		db.Model(&models.TenantPaymentMethod{}).
			Where("tenant_id = ?", tenantID).
			Count(&count).Error)
	assert.EqualValues(t, 1, count,
		"existing methods must not trigger an Efectivo seed")

	// Run a second time on a fresh orphan and confirm only one row appears.
	orphan := uuid.NewString()
	insertTenant(t, db, orphan)
	require.NoError(t, database.SeedDefaultPaymentMethods(db))
	require.NoError(t, database.SeedDefaultPaymentMethods(db))

	require.NoError(t,
		db.Model(&models.TenantPaymentMethod{}).
			Where("tenant_id = ?", orphan).
			Count(&count).Error)
	assert.EqualValues(t, 1, count,
		"second invocation must be a no-op for the freshly-seeded tenant")
}

// ── Case 3: ?active=true filter returns only active methods ──────────────────

func TestListPaymentMethods_ActiveFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSeedTestDB(t)

	tenantID := uuid.NewString()
	insertTenant(t, db, tenantID)

	// Two methods: one active, one inactive. We persist the inactive
	// one with an explicit Update so GORM's "skip zero values on
	// create" rule (combined with default:true on the column) doesn't
	// silently flip our false back to true.
	require.NoError(t, db.Create(&models.TenantPaymentMethod{
		TenantID: tenantID, Name: "Efectivo", Provider: "cash", IsActive: true,
	}).Error)
	inactive := models.TenantPaymentMethod{
		TenantID: tenantID, Name: "Nequi", Provider: "nequi", IsActive: true,
	}
	require.NoError(t, db.Create(&inactive).Error)
	require.NoError(t, db.Model(&inactive).
		Update("is_active", false).Error)

	r := gin.New()
	r.GET("/payment-methods", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		handlers.ListPaymentMethods(db)(c)
	})

	// Default (no query param) returns BOTH — admin-screen contract.
	{
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/payment-methods", nil)
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		var resp struct {
			Data  []models.TenantPaymentMethod `json:"data"`
			Count int                          `json:"count"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 2, resp.Count, "default lists every method")
	}

	// ?active=true returns ONLY the active one.
	{
		w := httptest.NewRecorder()
		req := httptest.NewRequest(
			http.MethodGet, "/payment-methods?active=true", nil)
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		var resp struct {
			Data  []models.TenantPaymentMethod `json:"data"`
			Count int                          `json:"count"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, 1, resp.Count, "filter must drop inactive rows")
		assert.Equal(t, "Efectivo", resp.Data[0].Name)
		assert.True(t, resp.Data[0].IsActive)
	}
}

// ── Case 4: tenant_register inserts the default Efectivo row ─────────────────
//
// Requires real PostgreSQL because the Tenant model carries jsonb +
// json-serializer slice columns. Skips gracefully when the local
// docker-compose Postgres isn't running, matching the pattern in
// tenant_register_test.go.

func TestTenantRegister_SeedsDefaultEfectivo(t *testing.T) {
	db := setupTestDB(t) // skips when Docker DB not available
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhoneWithMethods(t, db, phone) })

	w := postJSON(setupRouter(db), defaultPayload(phone))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID, _ := resp["tenant_id"].(string)
	require.NotEmpty(t, tenantID, "register response must carry tenant_id")

	var methods []models.TenantPaymentMethod
	require.NoError(t,
		db.Where("tenant_id = ?", tenantID).Find(&methods).Error)
	require.Len(t, methods, 1,
		"new tenant must land with exactly one payment method (Efectivo)")
	assert.Equal(t, "Efectivo", methods[0].Name)
	assert.Equal(t, "cash", methods[0].Provider)
	assert.True(t, methods[0].IsActive)
}

// cleanupByPhoneWithMethods extends the shared cleanupByPhone helper
// by also deleting the seeded payment_methods row before nuking the
// tenant — Postgres FKs would otherwise reject the parent delete.
func cleanupByPhoneWithMethods(t *testing.T, db *gorm.DB, phone string) {
	t.Helper()
	var tenant models.Tenant
	if err := db.Unscoped().Where("phone = ?", phone).First(&tenant).Error; err == nil {
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.TenantPaymentMethod{})
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.Employee{})
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.RefreshToken{})
		db.Unscoped().Delete(&tenant)
	}
}

