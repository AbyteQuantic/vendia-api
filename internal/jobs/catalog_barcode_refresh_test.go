// Spec: specs/096-foto-referencia-verificada/spec.md
package jobs

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/services"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupCatalogRefreshDB hand-crafts both tables involved (Postgres-only
// `gen_random_uuid()`/`uuid` defaults break SQLite AutoMigrate).
func setupCatalogRefreshDB(t *testing.T) *gorm.DB {
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
	require.NoError(t, db.Exec(`
		CREATE TABLE products (
			id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL,
			barcode TEXT, image_url TEXT, deleted_at DATETIME
		);
	`).Error)
	return db
}

func seedProduct(t *testing.T, db *gorm.DB, tenantID, barcode, imageURL string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO products (id, tenant_id, barcode, image_url) VALUES (?, ?, ?, ?)`,
		uuid.NewString(), tenantID, barcode, imageURL).Error)
}

// TestDiscoverBarcodesNeedingPhotos_PrioritizesRealDemand verifies the
// discovery query only considers barcodes with tenants that lack a
// photo, ordered by how many distinct tenants need it.
func TestDiscoverBarcodesNeedingPhotos_PrioritizesRealDemand(t *testing.T) {
	db := setupCatalogRefreshDB(t)

	// 3 tenants need "7702090000012" (no image), 1 tenant needs "1111111111111".
	seedProduct(t, db, "t1", "7702090000012", "")
	seedProduct(t, db, "t2", "7702090000012", "")
	seedProduct(t, db, "t3", "7702090000012", "")
	seedProduct(t, db, "t4", "1111111111111", "")
	// This tenant already has a photo — its barcode must NOT be discovered.
	seedProduct(t, db, "t5", "2222222222222", "https://r2.vendia.store/x.jpg")

	fakeOFF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":1,"product":{"product_name":"Producto","image_front_url":"https://off.example/x.jpg"}}`))
	}))
	defer fakeOFF.Close()
	offSvc := services.NewOpenFoodFactsServiceWithBaseURL(fakeOFF.URL)

	discovered, err := DiscoverBarcodesNeedingPhotos(db, offSvc, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, discovered, "descubre los 2 barcodes con demanda real")

	var count int64
	db.Table("catalog_products").Where("barcode = ? AND status = ?", "2222222222222", "verified").Count(&count)
	assert.Equal(t, int64(0), count, "un barcode que ya tiene foto propia no se procesa")
}

// TestDiscoverBarcodesNeedingPhotos_SkipsAlreadyVerified verifies AC-06:
// a barcode already verified in catalog_products is never rediscovered,
// even if tenants still lack their own copy of the photo.
func TestDiscoverBarcodesNeedingPhotos_SkipsAlreadyVerified(t *testing.T) {
	db := setupCatalogRefreshDB(t)
	seedProduct(t, db, "t1", "7702090000012", "")
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, source, status, image_url)
		VALUES ('cp1', 'Coca-Cola', '7702090000012', 'off', 'verified', 'https://off.example/coca.jpg')
	`).Error)

	offHits := 0
	fakeOFF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fakeOFF.Close()
	offSvc := services.NewOpenFoodFactsServiceWithBaseURL(fakeOFF.URL)

	discovered, err := DiscoverBarcodesNeedingPhotos(db, offSvc, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, discovered)
	assert.Equal(t, 0, offHits, "un barcode ya verified nunca vuelve a golpear OFF")
}

// TestRefreshStaleCatalogEntries_MarksBrokenLinksStale verifies a
// verified entry whose image no longer responds gets status='stale'
// instead of being deleted or silently kept verified.
func TestRefreshStaleCatalogEntries_MarksBrokenLinksStale(t *testing.T) {
	db := setupCatalogRefreshDB(t)
	oldCheck := time.Now().Add(-40 * 24 * time.Hour) // > 1 mes
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, source, status, image_url, last_checked_at)
		VALUES ('cp1', 'Producto', '7702090000012', 'off', 'verified', ?, ?)`,
		"http://broken.example/x.jpg", oldCheck).Error)

	brokenImage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer brokenImage.Close()
	require.NoError(t, db.Exec(`UPDATE catalog_products SET image_url = ? WHERE id = 'cp1'`, brokenImage.URL).Error)

	touched, err := RefreshStaleCatalogEntries(db)
	require.NoError(t, err)
	assert.Equal(t, 1, touched)

	var status string
	db.Table("catalog_products").Where("id = ?", "cp1").Pluck("status", &status)
	assert.Equal(t, "stale", status)
}

// TestRefreshStaleCatalogEntries_SkipsRecentlyChecked verifies a
// verified entry checked less than a month ago (frecuencia mensual,
// decidida en /clarify) is left untouched.
func TestRefreshStaleCatalogEntries_SkipsRecentlyChecked(t *testing.T) {
	db := setupCatalogRefreshDB(t)
	recentCheck := time.Now().Add(-3 * 24 * time.Hour)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, source, status, image_url, last_checked_at)
		VALUES ('cp1', 'Producto', '7702090000012', 'off', 'verified', 'https://off.example/x.jpg', ?)`,
		recentCheck).Error)

	touched, err := RefreshStaleCatalogEntries(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)

	var status string
	db.Table("catalog_products").Where("id = ?", "cp1").Pluck("status", &status)
	assert.Equal(t, "verified", status)
}
