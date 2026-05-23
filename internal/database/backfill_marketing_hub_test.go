// Spec: specs/037-reel-capacidades-dashboard/spec.md
package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// setupF037BackfillDB builds a SQLite schema that mirrors only the
// columns the F037 backfills touch. The full Tenant struct can't
// AutoMigrate on SQLite (jsonb columns) so we hand-craft the table
// with the 5 enable_* flags F037 introduces plus stubs for the source
// tables (promotions, recipes, ingredients, work_orders,
// purchase_orders) — each carrying only the bare minimum (id +
// tenant_id) needed for the EXISTS clause in the backfill SQL.
func setupF037BackfillDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			enable_marketing_hub BOOLEAN NOT NULL DEFAULT 0,
			enable_recipes BOOLEAN NOT NULL DEFAULT 0,
			enable_supplies BOOLEAN NOT NULL DEFAULT 0,
			enable_furniture_jobs BOOLEAN NOT NULL DEFAULT 0,
			enable_purchase_orders BOOLEAN NOT NULL DEFAULT 0
		);
	`).Error)

	// Bare-bones source tables — each backfill only needs `tenant_id`
	// for its EXISTS clause; an `id` PK keeps the table valid.
	sources := []string{
		"promotions", "recipes", "ingredients",
		"work_orders", "purchase_orders",
	}
	for _, table := range sources {
		require.NoError(t, db.Exec(fmt.Sprintf(`
			CREATE TABLE %s (
				id TEXT PRIMARY KEY,
				tenant_id TEXT NOT NULL
			);
		`, table)).Error)
	}

	require.NoError(t, db.AutoMigrate(&models.BootstrapMarker{}))
	return db
}

func insertF037Tenant(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, created_at, updated_at) VALUES (?, ?, ?)`,
		id, time.Now(), time.Now()).Error)
}

func insertF037SourceRow(t *testing.T, db *gorm.DB, table, tenantID string) {
	t.Helper()
	require.NoError(t, db.Exec(
		fmt.Sprintf(`INSERT INTO %s (id, tenant_id) VALUES (?, ?)`, table),
		uuid.NewString(), tenantID).Error)
}

func f037Flag(t *testing.T, db *gorm.DB, tenantID, column string) bool {
	t.Helper()
	var enabled bool
	require.NoError(t, db.Raw(
		fmt.Sprintf(`SELECT %s FROM tenants WHERE id = ?`, column),
		tenantID).Scan(&enabled).Error)
	return enabled
}

// TestBackfillF037Capabilities_FlipsTenantsWithData verifies the
// happy-path contract for every reclassification: a tenant with at
// least one row in the source table gets the matching enable_* flag
// flipped to true; a tenant without data stays false. Mirrors the
// "preserve access to modules already in use" rationale in the F037
// risk register (R1, R4).
func TestBackfillF037Capabilities_FlipsTenantsWithData(t *testing.T) {
	cases := []struct {
		name   string
		table  string
		column string
	}{
		{"marketing_hub", "promotions", "enable_marketing_hub"},
		{"recipes", "recipes", "enable_recipes"},
		{"supplies", "ingredients", "enable_supplies"},
		{"furniture_jobs", "work_orders", "enable_furniture_jobs"},
		{"purchase_orders", "purchase_orders", "enable_purchase_orders"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupF037BackfillDB(t)
			withData := uuid.NewString()
			withoutData := uuid.NewString()
			insertF037Tenant(t, db, withData)
			insertF037Tenant(t, db, withoutData)
			insertF037SourceRow(t, db, tc.table, withData)

			_, err := BackfillF037Capabilities(db)
			require.NoError(t, err)

			assert.True(t, f037Flag(t, db, withData, tc.column),
				"tenant con datos en %s debe quedar con %s=true (F037 R1/R4)",
				tc.table, tc.column)
			assert.False(t, f037Flag(t, db, withoutData, tc.column),
				"tenant sin datos en %s debe quedar con %s=false",
				tc.table, tc.column)
		})
	}
}

// TestBackfillF037Capabilities_IsIdempotent verifies the BootstrapMarker
// guard makes a second invocation a no-op. After the first run, a brand-
// new tenant added with data must NOT have its flag flipped on the
// second call — exactly the F036 onboarding backfill pattern.
func TestBackfillF037Capabilities_IsIdempotent(t *testing.T) {
	db := setupF037BackfillDB(t)
	preExisting := uuid.NewString()
	insertF037Tenant(t, db, preExisting)
	insertF037SourceRow(t, db, "promotions", preExisting)

	// First run: pre-existing tenant gets flipped.
	_, err := BackfillF037Capabilities(db)
	require.NoError(t, err)
	require.True(t, f037Flag(t, db, preExisting, "enable_marketing_hub"))

	// A brand-new tenant lands AFTER the deploy carrying combo data —
	// e.g. an admin script imported a new tenant. The backfill must
	// NOT flip them; they discover the capability through the reel.
	postDeploy := uuid.NewString()
	insertF037Tenant(t, db, postDeploy)
	insertF037SourceRow(t, db, "promotions", postDeploy)

	// Second run: guarded by the marker.
	_, err = BackfillF037Capabilities(db)
	require.NoError(t, err)
	assert.False(t, f037Flag(t, db, postDeploy, "enable_marketing_hub"),
		"un tenant nuevo no debe ser tocado por el backfill en el segundo boot")
}

// TestBackfillF037Capabilities_RecordsMarkers verifies every backfill
// writes its BootstrapMarker row on success so subsequent boots
// short-circuit. Without this guard the backfill would re-run on every
// pod restart and clobber post-deploy state.
func TestBackfillF037Capabilities_RecordsMarkers(t *testing.T) {
	db := setupF037BackfillDB(t)
	insertF037Tenant(t, db, uuid.NewString())

	_, err := BackfillF037Capabilities(db)
	require.NoError(t, err)

	expected := []string{
		"f037_marketing_hub_backfill",
		"f037_recipes_backfill",
		"f037_supplies_backfill",
		"f037_furniture_jobs_backfill",
		"f037_purchase_orders_backfill",
	}
	for _, name := range expected {
		var marker models.BootstrapMarker
		require.NoError(t, db.Where("name = ?", name).First(&marker).Error,
			"falta marker %s tras el backfill", name)
		assert.False(t, marker.RanAt.IsZero(),
			"marker %s debe registrar el timestamp", name)
	}
}

// TestBackfillF037Capabilities_NoDataLeavesFlagsFalse verifies that a
// fresh deploy with zero data in any source table leaves every flag
// at false and still records the markers — so the next boot is a
// no-op rather than a re-scan.
func TestBackfillF037Capabilities_NoDataLeavesFlagsFalse(t *testing.T) {
	db := setupF037BackfillDB(t)
	tenant := uuid.NewString()
	insertF037Tenant(t, db, tenant)

	_, err := BackfillF037Capabilities(db)
	require.NoError(t, err)

	columns := []string{
		"enable_marketing_hub",
		"enable_recipes",
		"enable_supplies",
		"enable_furniture_jobs",
		"enable_purchase_orders",
	}
	for _, col := range columns {
		assert.False(t, f037Flag(t, db, tenant, col),
			"sin datos el flag %s debe permanecer en false", col)
	}

	// Markers still get written so the next boot short-circuits.
	var count int64
	require.NoError(t, db.Model(&models.BootstrapMarker{}).
		Where("name LIKE 'f037_%'").Count(&count).Error)
	assert.Equal(t, int64(5), count,
		"los 5 markers F037 deben quedar registrados aunque no haya datos")
}
