// Spec: specs/036-dashboard-adaptativo-onboarding/spec.md
package database

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// setupOnboardingDB builds a SQLite schema with a hand-crafted `tenants`
// table carrying the one column the backfill touches
// (onboarding_completed) plus the real BootstrapMarker model. The full
// Tenant model can't AutoMigrate on SQLite (jsonb columns).
func setupOnboardingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			onboarding_completed BOOLEAN NOT NULL DEFAULT 0
		);
	`).Error)
	require.NoError(t, db.AutoMigrate(&models.BootstrapMarker{}))
	return db
}

func insertOnboardingTenant(t *testing.T, db *gorm.DB, id string, completed bool) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, created_at, updated_at, onboarding_completed) VALUES (?, ?, ?, ?)`,
		id, time.Now(), time.Now(), completed).Error)
}

func onboardingState(t *testing.T, db *gorm.DB, id string) bool {
	t.Helper()
	var completed bool
	require.NoError(t, db.Raw(
		`SELECT onboarding_completed FROM tenants WHERE id = ?`, id).Scan(&completed).Error)
	return completed
}

// TestBackfillOnboardingCompleted_MarksExistingTenants verifies that on
// first run every pre-F036 tenant (onboarding_completed=false) is flipped
// to true so an established business never sees the wizard (Spec F036
// D4 / AC-08).
func TestBackfillOnboardingCompleted_MarksExistingTenants(t *testing.T) {
	db := setupOnboardingDB(t)
	tenantA := uuid.NewString()
	tenantB := uuid.NewString()
	insertOnboardingTenant(t, db, tenantA, false)
	insertOnboardingTenant(t, db, tenantB, false)

	touched, err := BackfillOnboardingCompleted(db)
	require.NoError(t, err)
	assert.Equal(t, 2, touched, "ambos tenants pre-existentes quedan marcados")

	assert.True(t, onboardingState(t, db, tenantA))
	assert.True(t, onboardingState(t, db, tenantB))
}

// TestBackfillOnboardingCompleted_RunsExactlyOnce verifies the backfill
// is a one-shot: a tenant registered AFTER the first run keeps
// onboarding_completed=false (it must still see the wizard). A blind
// re-run would wrongly flip that new tenant.
func TestBackfillOnboardingCompleted_RunsExactlyOnce(t *testing.T) {
	db := setupOnboardingDB(t)
	existing := uuid.NewString()
	insertOnboardingTenant(t, db, existing, false)

	// First run: marks the pre-existing tenant.
	touched, err := BackfillOnboardingCompleted(db)
	require.NoError(t, err)
	assert.Equal(t, 1, touched)

	// A brand-new tenant registers after the deploy — onboarding pending.
	newTenant := uuid.NewString()
	insertOnboardingTenant(t, db, newTenant, false)

	// Second run on a later boot: the guard short-circuits, the new
	// tenant is left untouched so it still sees the wizard.
	touched, err = BackfillOnboardingCompleted(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched, "el backfill no se vuelve a ejecutar")
	assert.False(t, onboardingState(t, db, newTenant),
		"un tenant nuevo conserva onboarding_completed=false")
}

// TestBackfillOnboardingCompleted_RecordsMarker verifies the one-shot
// guard row is written so subsequent boots short-circuit.
func TestBackfillOnboardingCompleted_RecordsMarker(t *testing.T) {
	db := setupOnboardingDB(t)
	insertOnboardingTenant(t, db, uuid.NewString(), false)

	_, err := BackfillOnboardingCompleted(db)
	require.NoError(t, err)

	var marker models.BootstrapMarker
	require.NoError(t, db.Where("name = ?", onboardingBackfillMarker).First(&marker).Error)
	assert.False(t, marker.RanAt.IsZero())
}
