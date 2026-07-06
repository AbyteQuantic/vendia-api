// Spec: specs/096-foto-referencia-verificada/spec.md (Adenda B)
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupCatalogReferenceDB hand-crafts catalog_products (Postgres-only
// gen_random_uuid() default breaks SQLite AutoMigrate) — same shape used
// across this package's other catalog tests.
func setupCatalogReferenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_products (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, normalized_name TEXT,
			brand TEXT, image_url TEXT, barcode TEXT, sku TEXT,
			presentation TEXT, content TEXT, category TEXT,
			is_ai_enhanced BOOLEAN DEFAULT false, source TEXT DEFAULT 'off',
			fetched_at DATETIME, created_at DATETIME, updated_at DATETIME,
			status TEXT DEFAULT 'pending', verified_at DATETIME,
			last_checked_at DATETIME, license TEXT, source_url TEXT
		);
	`).Error)
	return db
}

// TestFindCatalogReferenceImageURL_PendingIsAValidReference verifies a
// pending OFF backup (never promoted to 'verified' per Adenda A) is still
// a valid GENERATION reference — Adenda B only needs a real photo to
// anchor container shape/label/colors, not one eligible for suggestion.
func TestFindCatalogReferenceImageURL_PendingIsAValidReference(t *testing.T) {
	db := setupCatalogReferenceDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp1', 'Alcohol JGB', '7702560009525', 'https://off.example/pending.jpg', 'off', 'pending')
	`).Error)

	url, err := findCatalogReferenceImageURL(db, "7702560009525")
	require.NoError(t, err)
	assert.Equal(t, "https://off.example/pending.jpg", url)
}

// TestFindCatalogReferenceImageURL_PrefersVerifiedOverPending verifies the
// ORDER BY picks the verified row first when several exist for the same
// barcode (defensive — shouldn't normally happen since FindOrCreateCatalogProduct
// keys by barcode, but the ranking must still favor the strongest signal).
func TestFindCatalogReferenceImageURL_PrefersVerifiedOverPending(t *testing.T) {
	db := setupCatalogReferenceDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp-pending', 'Duplicado', '9999999999999', 'https://off.example/pending.jpg', 'off', 'pending')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp-verified', 'Duplicado', '9999999999999', 'https://r2.vendia.store/verified.jpg', 'user', 'verified')
	`).Error)

	url, err := findCatalogReferenceImageURL(db, "9999999999999")
	require.NoError(t, err)
	assert.Equal(t, "https://r2.vendia.store/verified.jpg", url)
}

// TestFindCatalogReferenceImageURL_NoMatch_ReturnsEmpty verifies no error
// and an empty string when nothing is on file — the caller must fall
// back to text-only generation silently.
func TestFindCatalogReferenceImageURL_NoMatch_ReturnsEmpty(t *testing.T) {
	db := setupCatalogReferenceDB(t)

	url, err := findCatalogReferenceImageURL(db, "0000000000000")
	require.NoError(t, err)
	assert.Empty(t, url)
}

// TestFindCatalogReferenceImageURL_EmptyBarcode_ReturnsEmpty verifies a
// product with no barcode never triggers a lookup/match.
func TestFindCatalogReferenceImageURL_EmptyBarcode_ReturnsEmpty(t *testing.T) {
	db := setupCatalogReferenceDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp2', 'Producto sin barcode', '', 'https://off.example/x.jpg', 'off', 'pending')
	`).Error)

	url, err := findCatalogReferenceImageURL(db, "")
	require.NoError(t, err)
	assert.Empty(t, url)
}

// TestFindCatalogReferenceImageURL_SkipsRowsWithoutImage verifies a
// catalog row for this barcode with no image_url is never returned as a
// (useless) reference.
func TestFindCatalogReferenceImageURL_SkipsRowsWithoutImage(t *testing.T) {
	db := setupCatalogReferenceDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp3', 'Sin foto todavía', '1111111111111', '', 'user', 'pending')
	`).Error)

	url, err := findCatalogReferenceImageURL(db, "1111111111111")
	require.NoError(t, err)
	assert.Empty(t, url)
}
