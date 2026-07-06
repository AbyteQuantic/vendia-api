// Spec: specs/096-foto-referencia-verificada/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupLookupBarcodeDB hand-crafts catalog_products (Postgres-only
// `gen_random_uuid()` default breaks SQLite AutoMigrate, same gotcha as
// backfill_catalog_status_test.go).
func setupLookupBarcodeDB(t *testing.T) *gorm.DB {
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

func mountLookupBarcode(db *gorm.DB, offSvc *services.OpenFoodFactsService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/products/lookup", handlers.LookupBarcode(db, offSvc))
	return r
}

// TestLookupBarcode_CacheHit_NeverCallsOFF verifies AC-05/AC-06: a barcode
// already verified in catalog_products is served straight from the DB —
// the OFF fake server must receive ZERO requests.
func TestLookupBarcode_CacheHit_NeverCallsOFF(t *testing.T) {
	db := setupLookupBarcodeDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, brand, image_url, barcode, category, source, status)
		VALUES ('cp1', 'Coca-Cola 400ml', 'Coca-Cola', 'https://off.example/coca.jpg', '7702090000012', 'bebidas', 'off', 'verified')
	`).Error)

	offHits := 0
	fakeOFF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fakeOFF.Close()
	offSvc := services.NewOpenFoodFactsServiceWithBaseURL(fakeOFF.URL)

	r := mountLookupBarcode(db, offSvc)
	w := doJSON(t, r, http.MethodGet, "/products/lookup?barcode=7702090000012", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 0, offHits, "un barcode ya verificado NUNCA debe golpear OFF")

	var resp struct {
		Data services.OFFProduct `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "Coca-Cola 400ml", resp.Data.Name)
	assert.Equal(t, "https://off.example/coca.jpg", resp.Data.ImageURL)
}

// TestLookupBarcode_CacheMiss_PersistsResult verifies AC-05: a barcode not
// yet in the catalog is fetched from OFF and the result is saved so a
// future lookup of the same barcode hits the cache.
func TestLookupBarcode_CacheMiss_PersistsResult(t *testing.T) {
	db := setupLookupBarcodeDB(t)

	fakeOFF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": 1,
			"product": {
				"product_name": "Galletas Festival",
				"brands": "Noel",
				"image_front_url": "https://off.example/festival.jpg",
				"categories": "snacks"
			}
		}`))
	}))
	defer fakeOFF.Close()
	offSvc := services.NewOpenFoodFactsServiceWithBaseURL(fakeOFF.URL)

	r := mountLookupBarcode(db, offSvc)
	w := doJSON(t, r, http.MethodGet, "/products/lookup?barcode=7701234567890", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var count int64
	db.Table("catalog_products").
		Where("barcode = ? AND status = ?", "7701234567890", "verified").
		Count(&count)
	assert.Equal(t, int64(1), count, "el resultado de OFF debe quedar guardado como verified")

	var license string
	db.Table("catalog_products").
		Where("barcode = ?", "7701234567890").
		Pluck("license", &license)
	assert.Equal(t, "CC-BY-SA", license)
}

// TestLookupBarcode_NotFoundInOFF_ReturnsNotFound verifies the existing
// 404 contract is preserved when OFF has no data for the barcode — no
// crash, no phantom catalog row.
func TestLookupBarcode_NotFoundInOFF_ReturnsNotFound(t *testing.T) {
	db := setupLookupBarcodeDB(t)

	fakeOFF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": 0}`))
	}))
	defer fakeOFF.Close()
	offSvc := services.NewOpenFoodFactsServiceWithBaseURL(fakeOFF.URL)

	r := mountLookupBarcode(db, offSvc)
	w := doJSON(t, r, http.MethodGet, "/products/lookup?barcode=0000000000000", nil)
	require.Equal(t, http.StatusNotFound, w.Code)

	var count int64
	db.Table("catalog_products").Where("barcode = ?", "0000000000000").Count(&count)
	assert.Equal(t, int64(0), count)
}
