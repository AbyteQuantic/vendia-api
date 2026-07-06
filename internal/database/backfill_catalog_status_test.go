// Spec: specs/096-foto-referencia-verificada/spec.md
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

// setupCatalogStatusDB hand-crafts the table instead of AutoMigrate: the
// real model's `id uuid DEFAULT gen_random_uuid()` is Postgres-only syntax
// that SQLite's CREATE TABLE rejects (same gotcha as other tests in this
// repo with Postgres-specific column defaults).
func setupCatalogStatusDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_products (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			normalized_name TEXT,
			brand TEXT,
			image_url TEXT,
			barcode TEXT,
			sku TEXT,
			presentation TEXT,
			content TEXT,
			category TEXT,
			is_ai_enhanced BOOLEAN DEFAULT false,
			source TEXT DEFAULT 'off',
			fetched_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME,
			status TEXT DEFAULT 'pending',
			verified_at DATETIME,
			last_checked_at DATETIME,
			license TEXT,
			source_url TEXT
		);
	`).Error)
	return db
}

// TestBackfillCatalogStatus_MarksExistingOFFRowsVerified verifies that a
// pre-existing catalog row already imported from Open Food Facts (with a
// real image) gets status='verified' + license='CC-BY-SA' so it becomes
// eligible for suggestion without waiting for the new discovery job to
// re-fetch it.
func TestBackfillCatalogStatus_MarksExistingOFFRowsVerified(t *testing.T) {
	db := setupCatalogStatusDB(t)
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.CatalogProduct{
		ID: id, Name: "Coca-Cola 400ml", Barcode: "7702090000012",
		ImageURL: "https://images.openfoodfacts.org/x.jpg", Source: "off",
	}).Error)

	touched, err := BackfillCatalogStatus(db)
	require.NoError(t, err)
	assert.Equal(t, 1, touched)

	var row models.CatalogProduct
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, "verified", row.Status)
	assert.Equal(t, "CC-BY-SA", row.License)
	assert.NotNil(t, row.VerifiedAt)
}

// TestBackfillCatalogStatus_SkipsRowsWithoutImage verifies a catalog row
// with no image_url is left pending — there's nothing to suggest.
func TestBackfillCatalogStatus_SkipsRowsWithoutImage(t *testing.T) {
	db := setupCatalogStatusDB(t)
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.CatalogProduct{
		ID: id, Name: "Sin foto", Barcode: "1111111111111", Source: "off",
	}).Error)

	touched, err := BackfillCatalogStatus(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)

	var row models.CatalogProduct
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, "pending", row.Status)
}

// TestBackfillCatalogStatus_SkipsUserContributedRows verifies a
// tenant-contributed image (source='user', AI-enhanced) is never marked
// "verified" by this backfill — only OFF-sourced rows carry the
// open-license guarantee this status represents.
func TestBackfillCatalogStatus_SkipsUserContributedRows(t *testing.T) {
	db := setupCatalogStatusDB(t)
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.CatalogProduct{
		ID: id, Name: "Contribuido por tenant", Barcode: "2222222222222",
		ImageURL: "https://r2.vendia.store/x.jpg", Source: "user",
	}).Error)

	touched, err := BackfillCatalogStatus(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)

	var row models.CatalogProduct
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, "pending", row.Status)
}

// TestBackfillCatalogStatus_Idempotent verifies a second run is a no-op.
func TestBackfillCatalogStatus_Idempotent(t *testing.T) {
	db := setupCatalogStatusDB(t)
	require.NoError(t, db.Create(&models.CatalogProduct{
		ID: uuid.NewString(), Name: "Coca-Cola", Barcode: "3333333333333",
		ImageURL: "https://images.openfoodfacts.org/x.jpg", Source: "off",
	}).Error)

	first, err := BackfillCatalogStatus(db)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	second, err := BackfillCatalogStatus(db)
	require.NoError(t, err)
	assert.Equal(t, 0, second, "una segunda corrida es no-op")
}

// TestBackfillCatalogStatus_DoesNotOverrideAlreadyStale verifies a row an
// operator/job already marked "stale" is never silently flipped back to
// verified by this backfill.
func TestBackfillCatalogStatus_DoesNotOverrideAlreadyStale(t *testing.T) {
	db := setupCatalogStatusDB(t)
	id := uuid.NewString()
	checkedAt := time.Now()
	require.NoError(t, db.Create(&models.CatalogProduct{
		ID: id, Name: "Enlace caído", Barcode: "4444444444444",
		ImageURL: "https://images.openfoodfacts.org/x.jpg", Source: "off",
		Status: "stale", LastCheckedAt: &checkedAt,
	}).Error)

	touched, err := BackfillCatalogStatus(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)

	var row models.CatalogProduct
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, "stale", row.Status)
}
