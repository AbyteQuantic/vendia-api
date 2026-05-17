// Spec: specs/008-planes-suscripcion-epayco/spec.md
package database

import (
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSeedDB builds a SQLite schema with the two columns
// SeedTenantSubscriptions touches: a `tenants` table (id only) and the
// real TenantSubscription model. The Tenant model itself can't
// AutoMigrate on SQLite (jsonb column), so we hand-craft the minimal
// tenants table.
func setupSeedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		);
	`).Error)
	require.NoError(t, db.AutoMigrate(&models.TenantSubscription{}))
	return db
}

func insertTenant(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, created_at, updated_at) VALUES (?, ?, ?)`,
		id, time.Now(), time.Now()).Error)
}

// TestSeedTenantSubscriptions_BackfillsMissingRows verifies AC-02: a
// tenant with no subscription row gets a 14-day TRIAL.
func TestSeedTenantSubscriptions_BackfillsMissingRows(t *testing.T) {
	db := setupSeedDB(t)
	tenantA := uuid.NewString()
	tenantB := uuid.NewString()
	insertTenant(t, db, tenantA)
	insertTenant(t, db, tenantB)

	before := time.Now()
	created, err := SeedTenantSubscriptions(db)
	require.NoError(t, err)
	assert.Equal(t, 2, created, "ambos tenants sin suscripción reciben una")

	for _, id := range []string{tenantA, tenantB} {
		var sub models.TenantSubscription
		require.NoError(t, db.Where("tenant_id = ?", id).First(&sub).Error)
		assert.Equal(t, models.SubscriptionStatusTrial, sub.Status)
		assert.Equal(t, models.SubscriptionPlanFree, sub.Plan)
		require.NotNil(t, sub.TrialEndsAt)
		assert.True(t, sub.TrialEndsAt.After(before.Add(13*24*time.Hour)),
			"trial de cortesía de ~14 días")
		assert.True(t, sub.TrialEndsAt.Before(before.Add(15*24*time.Hour)))
	}
}

// TestSeedTenantSubscriptions_Idempotent verifies the backfill is a
// no-op on tenants that already have a subscription — it must not
// reset an existing PRO/FREE row back to TRIAL.
func TestSeedTenantSubscriptions_Idempotent(t *testing.T) {
	db := setupSeedDB(t)
	tenantPro := uuid.NewString()
	insertTenant(t, db, tenantPro)

	// Tenant already PRO_ACTIVE — must be left untouched.
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantPro,
		Status:   models.SubscriptionStatusProActive,
		Plan:     models.SubscriptionPlanPro,
	}).Error)

	created, err := SeedTenantSubscriptions(db)
	require.NoError(t, err)
	assert.Equal(t, 0, created, "un tenant con suscripción no se vuelve a sembrar")

	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantPro).First(&sub).Error)
	assert.Equal(t, models.SubscriptionStatusProActive, sub.Status,
		"el backfill NO degrada un PRO existente")

	// Second run still a no-op.
	created2, err := SeedTenantSubscriptions(db)
	require.NoError(t, err)
	assert.Equal(t, 0, created2)
}

// TestSeedTenantSubscriptions_OnlyBackfillsMissing covers a mixed
// population: some tenants have a row, some don't.
func TestSeedTenantSubscriptions_OnlyBackfillsMissing(t *testing.T) {
	db := setupSeedDB(t)
	withSub := uuid.NewString()
	withoutSub := uuid.NewString()
	insertTenant(t, db, withSub)
	insertTenant(t, db, withoutSub)

	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: withSub,
		Status:   models.SubscriptionStatusFree,
		Plan:     models.SubscriptionPlanFree,
	}).Error)

	created, err := SeedTenantSubscriptions(db)
	require.NoError(t, err)
	assert.Equal(t, 1, created, "solo el tenant sin suscripción se siembra")

	var orphan models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", withoutSub).First(&orphan).Error)
	assert.Equal(t, models.SubscriptionStatusTrial, orphan.Status)

	var existing models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", withSub).First(&existing).Error)
	assert.Equal(t, models.SubscriptionStatusFree, existing.Status,
		"la fila FREE preexistente no se toca")
}

func TestSeedTenantSubscriptions_NoTenants(t *testing.T) {
	db := setupSeedDB(t)
	created, err := SeedTenantSubscriptions(db)
	require.NoError(t, err)
	assert.Equal(t, 0, created)
}
