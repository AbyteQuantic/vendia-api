// Spec: specs/096-foto-referencia-verificada/spec.md (Adenda A)
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupCatalogServiceDB hand-crafts catalog_products/catalog_images
// (Postgres-only `gen_random_uuid()` defaults break SQLite AutoMigrate).
func setupCatalogServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// id DEFAULT (lower(hex(randomblob(16)))) — SQLite pseudo-UUID, ya que
	// el default real `gen_random_uuid()` es solo de Postgres y GORM no
	// genera un ID client-side para estos dos modelos (no embeben
	// BaseModel). Sin este default, `id` queda NULL en SQLite y cualquier
	// JOIN/IN por id dejaría de emparejar filas.
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_products (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			name TEXT NOT NULL, normalized_name TEXT,
			brand TEXT, image_url TEXT, barcode TEXT, sku TEXT,
			presentation TEXT, content TEXT, category TEXT,
			is_ai_enhanced BOOLEAN DEFAULT false, source TEXT DEFAULT 'off',
			fetched_at DATETIME, created_at DATETIME, updated_at DATETIME,
			status TEXT DEFAULT 'pending', verified_at DATETIME,
			last_checked_at DATETIME, license TEXT, source_url TEXT
		);
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_images (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			catalog_product_id TEXT NOT NULL,
			image_url TEXT NOT NULL, storage_key TEXT NOT NULL DEFAULT '',
			created_by_tenant_id TEXT NOT NULL, is_accepted BOOLEAN DEFAULT false,
			created_at DATETIME, updated_at DATETIME
		);
	`).Error)
	return db
}

// TestShareProductPhotoToCatalog_FirstTenant_StaysPending verifies AC-08:
// a single tenant sharing a photo is NOT enough — the catalog product
// stays unverified (not suggested to other tenants) until a second,
// distinct tenant also shares.
func TestShareProductPhotoToCatalog_FirstTenant_StaysPending(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)

	err := svc.ShareProductPhotoToCatalog(
		"tenant-a", "7702090000012", "Coca-Cola 400ml", "Coca-Cola", "400ml", "", "bebidas",
		"https://r2.vendia.store/tenant-a/coca.jpg")
	require.NoError(t, err)

	var status string
	db.Table("catalog_products").Where("barcode = ?", "7702090000012").Pluck("status", &status)
	assert.Equal(t, "pending", status, "un solo tenant no verifica (AC-08)")
}

// TestShareProductPhotoToCatalog_BackfillsCatalogProductImageURL is a
// regression test: catalog_products.image_url was staying EMPTY after a
// tenant shared a photo — the photo only landed in catalog_images
// (is_accepted=true), never mirrored back onto the product row. That
// silently broke two features that both read catalog_products.image_url
// directly: ReferencePhotoByBarcode (Adenda A — nothing to suggest to
// other tenants even once verified) and findCatalogReferenceImageURL
// (Adenda B — nothing to anchor "Crear foto con IA" generation to).
// The first tenant's shared photo must backfill image_url immediately —
// Adenda B only needs SOME real photo, not a verified one.
func TestShareProductPhotoToCatalog_BackfillsCatalogProductImageURL(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)

	require.NoError(t, svc.ShareProductPhotoToCatalog(
		"tenant-a", "7702090000012", "Coca-Cola 400ml", "Coca-Cola", "400ml", "", "bebidas",
		"https://r2.vendia.store/tenant-a/coca.jpg"))

	var imageURL string
	db.Table("catalog_products").Where("barcode = ?", "7702090000012").Pluck("image_url", &imageURL)
	assert.Equal(t, "https://r2.vendia.store/tenant-a/coca.jpg", imageURL,
		"catalog_products.image_url debe reflejar la foto compartida, no quedar vacío")
}

// TestShareProductPhotoToCatalog_SecondDistinctTenant_Verifies verifies
// AC-09: a second, independent tenant sharing/confirming a photo for the
// SAME barcode promotes the catalog product to verified.
func TestShareProductPhotoToCatalog_SecondDistinctTenant_Verifies(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)

	require.NoError(t, svc.ShareProductPhotoToCatalog(
		"tenant-a", "7702090000012", "Coca-Cola 400ml", "Coca-Cola", "400ml", "", "bebidas",
		"https://r2.vendia.store/tenant-a/coca.jpg"))
	require.NoError(t, svc.ShareProductPhotoToCatalog(
		"tenant-b", "7702090000012", "Coca-Cola 400ml", "Coca-Cola", "400ml", "", "bebidas",
		"https://r2.vendia.store/tenant-b/coca.jpg"))

	var status string
	db.Table("catalog_products").Where("barcode = ?", "7702090000012").Pluck("status", &status)
	assert.Equal(t, "verified", status, "el segundo tenant distinto verifica (AC-09)")
}

// TestShareProductPhotoToCatalog_SameTenantTwice_NeverVerifiesAlone
// verifies the SAME tenant sharing twice (e.g. re-enhancing the photo)
// never counts as two independent confirmations.
func TestShareProductPhotoToCatalog_SameTenantTwice_NeverVerifiesAlone(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)

	require.NoError(t, svc.ShareProductPhotoToCatalog(
		"tenant-a", "7702090000012", "Coca-Cola 400ml", "Coca-Cola", "400ml", "", "bebidas",
		"https://r2.vendia.store/tenant-a/coca-v1.jpg"))
	require.NoError(t, svc.ShareProductPhotoToCatalog(
		"tenant-a", "7702090000012", "Coca-Cola 400ml", "Coca-Cola", "400ml", "", "bebidas",
		"https://r2.vendia.store/tenant-a/coca-v2.jpg"))

	var status string
	db.Table("catalog_products").Where("barcode = ?", "7702090000012").Pluck("status", &status)
	assert.Equal(t, "pending", status,
		"el mismo tenant compartiendo dos veces no cuenta como 2 confirmaciones independientes")

	var imgCount int64
	db.Table("catalog_images").Where("catalog_product_id IN (SELECT id FROM catalog_products WHERE barcode = ?)", "7702090000012").Count(&imgCount)
	assert.Equal(t, int64(1), imgCount, "no duplica la imagen del mismo tenant")
}

// TestShareProductPhotoToCatalog_RequiresBarcodeAndImage.
func TestShareProductPhotoToCatalog_RequiresBarcodeAndImage(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)

	err := svc.ShareProductPhotoToCatalog("tenant-a", "", "Producto", "", "", "", "", "https://x.jpg")
	assert.Error(t, err, "sin barcode no se puede compartir")

	err = svc.ShareProductPhotoToCatalog("tenant-a", "7702090000012", "Producto", "", "", "", "", "")
	assert.Error(t, err, "sin imagen no se puede compartir")
}
