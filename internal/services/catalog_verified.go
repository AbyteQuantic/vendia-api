// Spec: specs/096-foto-referencia-verificada/spec.md
package services

import (
	"errors"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// UpsertVerifiedCatalogProduct persists a fresh Open Food Facts hit into
// the shared catalog as status=verified, keyed by barcode. Shared by
// LookupBarcode (live cache-miss) and the catalog-image-refresh job
// (batch discovery) — both need the exact same "trust OFF, mark verified"
// behavior so a barcode looked up once is never re-fetched needlessly
// (AC-05/AC-06). A product with no barcode or no image is skipped — there
// is nothing worth caching.
func UpsertVerifiedCatalogProduct(db *gorm.DB, product OFFProduct) error {
	if product.Barcode == "" || product.ImageURL == "" {
		return nil
	}
	now := time.Now()
	sourceURL := "https://world.openfoodfacts.org/product/" + product.Barcode

	var existing models.CatalogProduct
	err := db.Where("barcode = ?", product.Barcode).First(&existing).Error
	if err == nil {
		return db.Model(&existing).Updates(map[string]any{
			"name": product.Name, "brand": product.Brand, "image_url": product.ImageURL,
			"category": product.Category, "source": "off", "status": "verified",
			"verified_at": now, "last_checked_at": now, "license": "CC-BY-SA",
			"source_url": sourceURL, "fetched_at": now,
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return db.Create(&models.CatalogProduct{
		Name: product.Name, Brand: product.Brand, ImageURL: product.ImageURL,
		Barcode: product.Barcode, Category: product.Category, Source: "off",
		Status: "verified", VerifiedAt: &now, LastCheckedAt: &now,
		License: "CC-BY-SA", SourceURL: sourceURL, FetchedAt: &now,
	}).Error
}
