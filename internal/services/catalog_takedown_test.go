// Spec: specs/098-aporte-automatico-fotos-colaborativo/spec.md (Adenda A)
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TakedownByBarcode retira TODAS las imágenes aportadas para ese barcode y
// demota el producto a 'stale' (deja de sugerirse), sin tocar products.
func TestTakedownByBarcode_RemovesFromSharedCatalog(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)

	// Producto de catálogo verificado con una imagen aportada por un tenant.
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp1', 'Coca-Cola', '7702005004467', 'https://r2/coca.jpg', 'user', 'verified')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_images (id, catalog_product_id, image_url, created_by_tenant_id, is_accepted)
		VALUES ('img1', 'cp1', 'https://r2/coca.jpg', 'tenant-a', true)
	`).Error)

	n, err := svc.TakedownByBarcode("7702005004467")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// El producto ya no se sugiere: status stale + image_url vacía.
	var status, img string
	db.Table("catalog_products").Where("id = ?", "cp1").Pluck("status", &status)
	db.Table("catalog_products").Where("id = ?", "cp1").Pluck("image_url", &img)
	assert.Equal(t, "stale", status)
	assert.Equal(t, "", img)

	// La imagen del catálogo se retiró.
	var count int64
	db.Table("catalog_images").Where("catalog_product_id = ?", "cp1").Count(&count)
	assert.EqualValues(t, 0, count)
}

func TestTakedownByBarcode_NotFound(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)
	_, err := svc.TakedownByBarcode("0000000000000")
	assert.Error(t, err)
}

// TakedownByImageID retira una imagen y, si era la sugerida del producto, lo demota.
func TestTakedownByImageID_DemotesWhenItWasTheSuggested(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)

	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, image_url, source, status)
		VALUES ('cp2', 'Salsa', '7501031311309', 'https://off/salsa.jpg', 'user', 'verified')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_images (id, catalog_product_id, image_url, created_by_tenant_id, is_accepted)
		VALUES ('img2', 'cp2', 'https://off/salsa.jpg', 'tenant-b', true)
	`).Error)

	require.NoError(t, svc.TakedownByImageID("img2"))

	var count int64
	db.Table("catalog_images").Where("id = ?", "img2").Count(&count)
	assert.EqualValues(t, 0, count)

	var status, img string
	db.Table("catalog_products").Where("id = ?", "cp2").Pluck("status", &status)
	db.Table("catalog_products").Where("id = ?", "cp2").Pluck("image_url", &img)
	assert.Equal(t, "stale", status)
	assert.Equal(t, "", img)
}

func TestTakedown_EmptyArgs(t *testing.T) {
	db := setupCatalogServiceDB(t)
	svc := NewCatalogService(db, nil)
	_, err := svc.TakedownByBarcode("")
	assert.Error(t, err)
	assert.Error(t, svc.TakedownByImageID(""))
}
