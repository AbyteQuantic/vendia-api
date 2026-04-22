package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Pure-function tests for the transformer. The DB-backed end-to-end
// test (TestAdminListTenants_E2E) lives in the postgres-gated
// integration suite via the existing setupTestDB helper — SQLite
// can't parse the Tenant model's `default:gen_random_uuid()` clause
// so we exercise the handler here with a curated Tenant struct that
// bypasses the full AutoMigrate.

func TestBuildGodModeTenants_TransformsRawRowsIntoResponseShape(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)

	tenants := []models.Tenant{
		{OwnerName: "Pedro Martínez", BusinessName: "Tienda Don Pedro",
			BusinessTypes: []string{"tienda_barrio"},
			Address:       "Calle 5 #12-34, Medellín"},
		{OwnerName: "Ana Gómez", BusinessName: "Taller Ana",
			BusinessTypes: []string{"reparacion_muebles"},
			Address:       "Cra 10 #20-30, Bogotá"},
	}
	tenants[0].ID = "tenant-1"
	tenants[1].ID = "tenant-2"

	subs := map[string]models.TenantSubscription{
		"tenant-1": {TenantID: "tenant-1",
			Status:      models.SubscriptionStatusTrial,
			TrialEndsAt: ptrTime(now.Add(3 * 24 * time.Hour))},
		"tenant-2": {TenantID: "tenant-2",
			Status: models.SubscriptionStatusProActive},
	}

	rows := handlers.BuildGodModeTenants(
		tenants, subs,
		map[string]int{"tenant-1": 1, "tenant-2": 3},
		map[string]int{"tenant-1": 2, "tenant-2": 7},
		now,
	)

	require.Len(t, rows, 2)

	t1 := rows[0]
	assert.Equal(t, "Tienda Don Pedro", t1.BusinessName)
	assert.Equal(t, "tienda_barrio", t1.BusinessType)
	assert.Equal(t, "Calle 5 #12-34, Medellín", t1.Location)
	assert.Equal(t, 1, t1.BranchesCount)
	assert.Equal(t, 2, t1.EmployeesCount)
	assert.Equal(t, models.SubscriptionStatusTrial, t1.SubscriptionStatus)
	assert.Equal(t, 3, t1.TrialDaysRemaining)
	assert.True(t, t1.IsPremium, "active trial must count as premium")

	t2 := rows[1]
	assert.Equal(t, "Taller Ana", t2.BusinessName)
	assert.Equal(t, 3, t2.BranchesCount)
	assert.Equal(t, 7, t2.EmployeesCount)
	assert.Equal(t, models.SubscriptionStatusProActive, t2.SubscriptionStatus)
	assert.Equal(t, 0, t2.TrialDaysRemaining,
		"PRO_ACTIVE is not a trial — remaining days must be 0")
	assert.True(t, t2.IsPremium)
}

func TestBuildGodModeTenants_FallsBackToFreeWhenSubscriptionMissing(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)

	tenants := []models.Tenant{
		{OwnerName: "Legacy", BusinessName: "Legacy Store",
			BusinessTypes: []string{"tienda_barrio"}, Address: "—"},
	}
	tenants[0].ID = "legacy-tenant"

	rows := handlers.BuildGodModeTenants(
		tenants,
		map[string]models.TenantSubscription{}, // no row
		map[string]int{}, map[string]int{}, now,
	)

	require.Len(t, rows, 1)
	assert.Equal(t, models.SubscriptionStatusFree, rows[0].SubscriptionStatus,
		"missing subscription row degrades to FREE so the dashboard doesn't render blank")
	assert.False(t, rows[0].IsPremium)
	assert.Equal(t, 0, rows[0].TrialDaysRemaining)
}

func TestBuildGodModeTenants_EmptyInput(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	rows := handlers.BuildGodModeTenants(
		nil, map[string]models.TenantSubscription{},
		map[string]int{}, map[string]int{}, now,
	)
	assert.Empty(t, rows)
}

func TestBuildGodModeTenants_ExpiredTrialReportsZeroDaysAndNotPremium(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	tenants := []models.Tenant{
		{OwnerName: "Expired", BusinessName: "Expired Biz",
			BusinessTypes: []string{"tienda_barrio"}, Address: "Cali"},
	}
	tenants[0].ID = "expired-tenant"

	subs := map[string]models.TenantSubscription{
		"expired-tenant": {
			TenantID:    "expired-tenant",
			Status:      models.SubscriptionStatusTrial,
			TrialEndsAt: ptrTime(now.Add(-1 * time.Hour)),
		},
	}

	rows := handlers.BuildGodModeTenants(
		tenants, subs, map[string]int{}, map[string]int{}, now,
	)

	require.Len(t, rows, 1)
	// Status is still TRIAL in the row (the middleware is what flips it
	// to FREE on write-through). The dashboard uses `is_premium` as the
	// gate rather than the label, so displaying "TRIAL — 0 days left"
	// is both accurate and a strong upsell trigger.
	assert.Equal(t, models.SubscriptionStatusTrial, rows[0].SubscriptionStatus)
	assert.Equal(t, 0, rows[0].TrialDaysRemaining)
	assert.False(t, rows[0].IsPremium)
}

// AdminListTenants handler-level test using an in-memory sqlite DB.
// The Tenant model's `default:gen_random_uuid()` clause fails on
// SQLite, so we hand-craft a narrow tenants table with just the
// columns the handler SELECTs. Good enough to exercise the join +
// aggregate pipeline without a full Postgres dependency.
func setupAdminTenantsSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			created_at DATETIME NOT NULL,
			updated_at DATETIME,
			deleted_at DATETIME,
			owner_name TEXT NOT NULL,
			phone TEXT NOT NULL,
			business_name TEXT NOT NULL,
			business_types TEXT NOT NULL DEFAULT '[]',
			address TEXT NOT NULL DEFAULT '',
			subscription_status TEXT DEFAULT 'trial',
			subscription_ends_at DATETIME,
			last_sync_at DATETIME,
			pending_sync_ops INTEGER DEFAULT 0
		);
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE branches (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			name TEXT NOT NULL,
			address TEXT DEFAULT '',
			is_active INTEGER DEFAULT 1
		);
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE employees (
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			name TEXT NOT NULL,
			phone TEXT,
			pin TEXT,
			role TEXT NOT NULL DEFAULT 'cashier'
		);
	`).Error)
	require.NoError(t, db.AutoMigrate(&models.TenantSubscription{}))
	return db
}

func TestAdminListTenants_E2E_GodModeShapeFromSQLite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupAdminTenantsSQLite(t)
	now := time.Now().UTC()
	trialEnd := now.Add(5 * 24 * time.Hour)

	// Seed tenants via raw SQL to sidestep the Tenant BaseModel hooks.
	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, created_at, owner_name, phone, business_name,
			business_types, address)
		VALUES
			('tenant-trial', ?, 'Trial Owner', '3001111111', 'Trial Biz',
			 '["tienda_barrio"]', 'Medellín'),
			('tenant-pro',   ?, 'Pro Owner',   '3002222222', 'Pro Biz',
			 '["restaurante"]',    'Bogotá'),
			('tenant-legacy', ?, 'Legacy Owner','3003333333', 'Legacy Biz',
			 '["minimercado"]',   'Cali')
	`, now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-1*time.Hour)).Error)

	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: "tenant-trial", Status: models.SubscriptionStatusTrial,
		TrialEndsAt: &trialEnd, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: "tenant-pro", Status: models.SubscriptionStatusProActive,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	// legacy tenant intentionally has no subscription row

	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, tenant_id, name) VALUES
			('br1','tenant-pro','Sede Centro'),
			('br2','tenant-pro','Sede Norte')`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO employees (id, tenant_id, name, role) VALUES
			('em1','tenant-pro','Empleada 1','cashier')`).Error)

	r := gin.New()
	r.GET("/api/v1/admin/tenants", handlers.AdminListTenants(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/tenants", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []handlers.GodModeTenantRow `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data, 3)

	byName := map[string]handlers.GodModeTenantRow{}
	for _, row := range body.Data {
		byName[row.BusinessName] = row
	}

	trial := byName["Trial Biz"]
	assert.Equal(t, "Medellín", trial.Location)
	assert.Equal(t, models.SubscriptionStatusTrial, trial.SubscriptionStatus)
	assert.GreaterOrEqual(t, trial.TrialDaysRemaining, 5)
	assert.True(t, trial.IsPremium)

	pro := byName["Pro Biz"]
	assert.Equal(t, models.SubscriptionStatusProActive, pro.SubscriptionStatus)
	assert.Equal(t, 2, pro.BranchesCount)
	assert.Equal(t, 1, pro.EmployeesCount)
	assert.True(t, pro.IsPremium)

	legacy := byName["Legacy Biz"]
	assert.Equal(t, models.SubscriptionStatusFree, legacy.SubscriptionStatus,
		"missing subscription row surfaces as FREE")
	assert.False(t, legacy.IsPremium)
	assert.Equal(t, 0, legacy.BranchesCount)
	assert.Equal(t, 0, legacy.EmployeesCount)
}

func ptrTime(t time.Time) *time.Time { return &t }
