// Spec: specs/096-foto-referencia-verificada/spec.md
package jobs

import (
	"context"
	"net/http"
	"time"

	"vendia-backend/internal/services"

	"gorm.io/gorm"
)

// DiscoverBarcodesNeedingPhotos finds barcodes with real demand — products
// across tenants that have a barcode but no photo yet — and are NOT
// already verified in the shared catalog, then fetches each from Open
// Food Facts and caches the result (AC-06: never reprocesses what's
// already verified). Ordered by how many distinct tenants need it, so the
// catalog grows where it helps the most tenants first. Returns how many
// barcodes were newly discovered (regardless of whether OFF had a photo
// for them).
func DiscoverBarcodesNeedingPhotos(db *gorm.DB, offSvc *services.OpenFoodFactsService, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}

	var barcodes []string
	err := db.Table("products").
		Select("barcode").
		Where("barcode IS NOT NULL AND barcode != '' AND (image_url IS NULL OR image_url = '') AND deleted_at IS NULL").
		Where("barcode NOT IN (SELECT barcode FROM catalog_products WHERE status = 'verified')").
		Group("barcode").
		Order("COUNT(DISTINCT tenant_id) DESC").
		Limit(limit).
		Pluck("barcode", &barcodes).Error
	if err != nil {
		return 0, err
	}

	discovered := 0
	for _, barcode := range barcodes {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		product, err := offSvc.LookupBarcode(ctx, barcode)
		cancel()
		if err != nil || product == nil {
			continue
		}
		if upsertErr := services.UpsertVerifiedCatalogProduct(db, *product); upsertErr == nil {
			discovered++
		}
		time.Sleep(300 * time.Millisecond) // cortesía con la API pública de OFF
	}
	return discovered, nil
}

// RefreshStaleCatalogEntries re-checks verified catalog photos whose
// last_checked_at is older than the monthly cadence (decidido en
// /clarify) — if the image no longer responds, marks the row 'stale' so
// it stops being suggested without deleting the historical record.
// Returns how many rows were marked stale.
func RefreshStaleCatalogEntries(db *gorm.DB) (int, error) {
	threshold := time.Now().Add(-30 * 24 * time.Hour)

	type row struct {
		ID       string
		ImageURL string
	}
	var rows []row
	err := db.Table("catalog_products").
		Select("id, image_url").
		Where("status = ? AND (last_checked_at IS NULL OR last_checked_at < ?)", "verified", threshold).
		Find(&rows).Error
	if err != nil {
		return 0, err
	}

	client := &http.Client{Timeout: 8 * time.Second}
	staleCount := 0
	now := time.Now()
	for _, r := range rows {
		if imageStillReachable(client, r.ImageURL) {
			db.Table("catalog_products").Where("id = ?", r.ID).Update("last_checked_at", now)
			continue
		}
		db.Table("catalog_products").Where("id = ?", r.ID).Updates(map[string]any{
			"status": "stale", "last_checked_at": now,
		})
		staleCount++
	}
	return staleCount, nil
}

func imageStillReachable(client *http.Client, imageURL string) bool {
	if imageURL == "" {
		return false
	}
	resp, err := client.Head(imageURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
