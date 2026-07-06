// Spec: specs/096-foto-referencia-verificada/spec.md (Adenda A)
package database

import (
	"testing"

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

// TestRevertOffAutoVerifiedCatalogRows_RevertsOffVerifiedRows verifies the
// Adenda A correction: an OFF-sourced row that a prior boot's (now removed)
// backfill wrongly marked 'verified' with zero tenant confirmation gets
// reverted back to 'pending'.
func TestRevertOffAutoVerifiedCatalogRows_RevertsOffVerifiedRows(t *testing.T) {
	db := setupCatalogStatusDB(t)
	id := uuid.NewString()
	now := "2026-07-01 00:00:00"
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status, verified_at)
		VALUES (?, 'Coca-Cola 400ml', '7702090000012', 'https://images.openfoodfacts.org/x.jpg', 'off', 'verified', ?)
	`, id, now).Error)

	touched, err := RevertOffAutoVerifiedCatalogRows(db)
	require.NoError(t, err)
	assert.Equal(t, 1, touched)

	var row models.CatalogProduct
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, "pending", row.Status)
	assert.Nil(t, row.VerifiedAt)
}

// TestRevertOffAutoVerifiedCatalogRows_NeverTouchesUserVerifiedRows
// verifies a row verified through real tenant consensus (source='user',
// set by CatalogService.ShareProductPhotoToCatalog) is left untouched —
// this correction only targets the old OFF-only mistake.
func TestRevertOffAutoVerifiedCatalogRows_NeverTouchesUserVerifiedRows(t *testing.T) {
	db := setupCatalogStatusDB(t)
	id := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES (?, 'Coca-Cola 400ml', '7702090000012', 'https://r2.vendia.store/x.jpg', 'user', 'verified')
	`, id).Error)

	touched, err := RevertOffAutoVerifiedCatalogRows(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)

	var row models.CatalogProduct
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, "verified", row.Status)
}

// TestRevertOffAutoVerifiedCatalogRows_Idempotent verifies a second run is
// a no-op — once reverted, a row is 'pending' and never matches again.
func TestRevertOffAutoVerifiedCatalogRows_Idempotent(t *testing.T) {
	db := setupCatalogStatusDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES (?, 'Coca-Cola', '3333333333333', 'https://images.openfoodfacts.org/x.jpg', 'off', 'verified')
	`, uuid.NewString()).Error)

	first, err := RevertOffAutoVerifiedCatalogRows(db)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	second, err := RevertOffAutoVerifiedCatalogRows(db)
	require.NoError(t, err)
	assert.Equal(t, 0, second, "una segunda corrida es no-op")
}

// TestRevertOffAutoVerifiedCatalogRows_SkipsPendingAndStaleRows verifies
// rows that were never verified are left alone.
func TestRevertOffAutoVerifiedCatalogRows_SkipsPendingAndStaleRows(t *testing.T) {
	db := setupCatalogStatusDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES (?, 'Pendiente', '4444444444444', 'https://images.openfoodfacts.org/x.jpg', 'off', 'pending')
	`, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES (?, 'Caído', '5555555555555', 'https://images.openfoodfacts.org/x.jpg', 'off', 'stale')
	`, uuid.NewString()).Error)

	touched, err := RevertOffAutoVerifiedCatalogRows(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)
}
