// Spec: specs/096-foto-referencia-verificada/spec.md
package services

import (
	"errors"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// UpsertOffCatalogBackup persists a fresh Open Food Facts hit into the
// shared catalog as a BACKUP candidate — status='pending', never
// 'verified'. Adenda A (2026-07-06): el fundador probó OFF en producción
// y encontró cobertura pobre de productos colombianos + fotos de mala
// calidad — OFF nunca debe ser la fuente de verdad. Solo el consenso de
// 2+ tenants compartiendo su propia foto (ShareProductPhotoToCatalog)
// marca una fila 'verified' y por lo tanto sugerible (Spec 096 FR-10/11).
//
// Si el barcode ya pertenece a un tenant (source='user'), esta función no
// toca la fila en absoluto — un dato externo nunca debe pisar una foto
// real ya aportada por un tendero.
//
// Shared by LookupBarcode (live cache-miss) and the catalog-image-refresh
// job (batch discovery) — both need the exact same "guardar como
// respaldo, sin verificar" behavior so un barcode consultado una vez no
// se vuelve a golpear la próxima vez (AC-05). A product with no barcode
// or no image is skipped — there is nothing worth caching.
func UpsertOffCatalogBackup(db *gorm.DB, product OFFProduct) error {
	if product.Barcode == "" || product.ImageURL == "" {
		return nil
	}
	now := time.Now()
	sourceURL := "https://world.openfoodfacts.org/product/" + product.Barcode

	var existing models.CatalogProduct
	err := db.Where("barcode = ?", product.Barcode).First(&existing).Error
	if err == nil {
		if existing.Source == "user" {
			return nil // el tenant ya es dueño de esta fila — OFF no la toca
		}
		return db.Model(&existing).Updates(map[string]any{
			"name": product.Name, "brand": product.Brand, "image_url": product.ImageURL,
			"category": product.Category, "source": "off",
			"last_checked_at": now, "license": "CC-BY-SA",
			"source_url": sourceURL, "fetched_at": now,
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return db.Create(&models.CatalogProduct{
		Name: product.Name, Brand: product.Brand, ImageURL: product.ImageURL,
		Barcode: product.Barcode, Category: product.Category, Source: "off",
		Status: "pending", LastCheckedAt: &now,
		License: "CC-BY-SA", SourceURL: sourceURL, FetchedAt: &now,
	}).Error
}
