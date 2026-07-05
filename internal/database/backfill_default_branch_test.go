// Spec: specs/014-inventario-solido-scope-sede/spec.md
package database

import (
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBackfillDefaultBranch_MarksOldestBranch verifies the incident fix:
// a tenant with 2+ branches and no is_default gets its OLDEST branch
// (by created_at) marked as default — the same tie-breaker used by
// BackfillBranchIDs, so both backfills agree on "the sede that holds
// the tenant's real data".
func TestBackfillDefaultBranch_MarksOldestBranch(t *testing.T) {
	db := setupBackfillDB(t)
	tenantID := uuid.NewString()
	insertBackfillTenant(t, db, tenantID)

	now := time.Now()
	oldBranch := seedBranch(t, db, tenantID, now.Add(-48*time.Hour))
	newBranch := seedBranch(t, db, tenantID, now.Add(-1*time.Hour))

	touched, err := BackfillDefaultBranch(db)
	require.NoError(t, err)
	assert.Equal(t, 1, touched)

	var old, new_ models.Branch
	require.NoError(t, db.First(&old, "id = ?", oldBranch).Error)
	require.NoError(t, db.First(&new_, "id = ?", newBranch).Error)
	assert.True(t, old.IsDefault, "la sede más antigua queda marcada por defecto")
	assert.False(t, new_.IsDefault, "la sede más nueva NO se toca")
}

// TestBackfillDefaultBranch_SkipsTenantWithExistingDefault verifies a
// tenant that already has an is_default=true branch is left untouched —
// this is what makes the backfill safe to run on every boot without
// ever overriding a choice already made (e.g. a brand-new tenant whose
// "Principal" branch was created with is_default=true at registration).
func TestBackfillDefaultBranch_SkipsTenantWithExistingDefault(t *testing.T) {
	db := setupBackfillDB(t)
	tenantID := uuid.NewString()
	insertBackfillTenant(t, db, tenantID)

	now := time.Now()
	oldBranch := seedBranch(t, db, tenantID, now.Add(-48*time.Hour))
	newBranch := seedBranch(t, db, tenantID, now.Add(-1*time.Hour))
	// The NEWER branch is explicitly the chosen default (e.g. an owner
	// picked it manually) — the backfill must not fight that choice.
	require.NoError(t, db.Model(&models.Branch{}).
		Where("id = ?", newBranch).Update("is_default", true).Error)

	touched, err := BackfillDefaultBranch(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched, "el tenant ya tiene default — no se toca")

	var old, new_ models.Branch
	require.NoError(t, db.First(&old, "id = ?", oldBranch).Error)
	require.NoError(t, db.First(&new_, "id = ?", newBranch).Error)
	assert.False(t, old.IsDefault)
	assert.True(t, new_.IsDefault, "la elección explícita se conserva")
}

// TestBackfillDefaultBranch_Idempotent verifies a second run after the
// first is a no-op.
func TestBackfillDefaultBranch_Idempotent(t *testing.T) {
	db := setupBackfillDB(t)
	tenantID := uuid.NewString()
	insertBackfillTenant(t, db, tenantID)
	seedBranch(t, db, tenantID, time.Now().Add(-time.Hour))

	first, err := BackfillDefaultBranch(db)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	second, err := BackfillDefaultBranch(db)
	require.NoError(t, err)
	assert.Equal(t, 0, second, "una segunda corrida es no-op")
}

// TestBackfillDefaultBranch_NoData verifies the function is safe on an
// empty database.
func TestBackfillDefaultBranch_NoData(t *testing.T) {
	db := setupBackfillDB(t)
	touched, err := BackfillDefaultBranch(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)
}

// TestBackfillDefaultBranch_MultiTenant verifies isolation: each
// tenant's default is resolved independently — one tenant's branches
// never influence another's (Art. III).
func TestBackfillDefaultBranch_MultiTenant(t *testing.T) {
	db := setupBackfillDB(t)
	tenantA := uuid.NewString()
	tenantB := uuid.NewString()
	insertBackfillTenant(t, db, tenantA)
	insertBackfillTenant(t, db, tenantB)

	now := time.Now()
	branchA := seedBranch(t, db, tenantA, now.Add(-2*time.Hour))
	branchB := seedBranch(t, db, tenantB, now.Add(-3*time.Hour))

	touched, err := BackfillDefaultBranch(db)
	require.NoError(t, err)
	assert.Equal(t, 2, touched)

	var a, b models.Branch
	require.NoError(t, db.First(&a, "id = ?", branchA).Error)
	require.NoError(t, db.First(&b, "id = ?", branchB).Error)
	assert.True(t, a.IsDefault)
	assert.True(t, b.IsDefault)
}
