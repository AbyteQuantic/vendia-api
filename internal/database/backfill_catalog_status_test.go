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

// setupCatalogImageURLBackfillDB adds catalog_images alongside
// catalog_products for the Adenda B image_url-repair tests below.
func setupCatalogImageURLBackfillDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupCatalogStatusDB(t)
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_images (
			id TEXT PRIMARY KEY, catalog_product_id TEXT NOT NULL,
			image_url TEXT NOT NULL, storage_key TEXT NOT NULL DEFAULT '',
			created_by_tenant_id TEXT NOT NULL, is_accepted BOOLEAN DEFAULT false,
			created_at DATETIME, updated_at DATETIME
		);
	`).Error)
	return db
}

// TestBackfillCatalogProductImageURL_RepairsEmptyImageURL verifies the
// Adenda B regression fix: a catalog_products row left with an empty
// image_url (the bug) gets it backfilled from its accepted catalog_image.
func TestBackfillCatalogProductImageURL_RepairsEmptyImageURL(t *testing.T) {
	db := setupCatalogImageURLBackfillDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp1', 'Camiseta QA', '7501234567893', '', 'user', 'pending')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_images (id, catalog_product_id, image_url, created_by_tenant_id, is_accepted, created_at)
		VALUES ('ci1', 'cp1', 'https://r2.vendia.store/tenant-a/camiseta.png', 'tenant-a', true, '2026-07-06 00:00:00')
	`).Error)

	touched, err := BackfillCatalogProductImageURL(db)
	require.NoError(t, err)
	assert.Equal(t, 1, touched)

	var imageURL string
	db.Table("catalog_products").Where("id = ?", "cp1").Pluck("image_url", &imageURL)
	assert.Equal(t, "https://r2.vendia.store/tenant-a/camiseta.png", imageURL)
}

// TestBackfillCatalogProductImageURL_SkipsRowsAlreadyHavingAnImage
// verifies a row that already has an image_url (OFF-sourced, or already
// repaired) is never overwritten.
func TestBackfillCatalogProductImageURL_SkipsRowsAlreadyHavingAnImage(t *testing.T) {
	db := setupCatalogImageURLBackfillDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp2', 'Coca-Cola', '7702090000012', 'https://images.openfoodfacts.org/x.jpg', 'off', 'pending')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_images (id, catalog_product_id, image_url, created_by_tenant_id, is_accepted, created_at)
		VALUES ('ci2', 'cp2', 'https://r2.vendia.store/tenant-a/coca.png', 'tenant-a', true, '2026-07-06 00:00:00')
	`).Error)

	touched, err := BackfillCatalogProductImageURL(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)

	var imageURL string
	db.Table("catalog_products").Where("id = ?", "cp2").Pluck("image_url", &imageURL)
	assert.Equal(t, "https://images.openfoodfacts.org/x.jpg", imageURL,
		"un image_url ya existente nunca se pisa")
}

// TestBackfillCatalogProductImageURL_SkipsRowsWithNoAcceptedImage
// verifies a row with an empty image_url but NO accepted catalog_image
// (nothing to repair from) is left as-is, no error.
func TestBackfillCatalogProductImageURL_SkipsRowsWithNoAcceptedImage(t *testing.T) {
	db := setupCatalogImageURLBackfillDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp3', 'Sin fotos', '1111111111111', '', 'user', 'pending')
	`).Error)

	touched, err := BackfillCatalogProductImageURL(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)
}

// TestBackfillCatalogProductImageURL_Idempotent verifies a second run is
// a no-op — once repaired, a row's image_url is non-empty and never
// matches the backfill's WHERE clause again.
func TestBackfillCatalogProductImageURL_Idempotent(t *testing.T) {
	db := setupCatalogImageURLBackfillDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp4', 'Camiseta QA', '7501234567893', '', 'user', 'pending')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_images (id, catalog_product_id, image_url, created_by_tenant_id, is_accepted, created_at)
		VALUES ('ci4', 'cp4', 'https://r2.vendia.store/tenant-a/camiseta.png', 'tenant-a', true, '2026-07-06 00:00:00')
	`).Error)

	first, err := BackfillCatalogProductImageURL(db)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	second, err := BackfillCatalogProductImageURL(db)
	require.NoError(t, err)
	assert.Equal(t, 0, second, "una segunda corrida es no-op")
}
