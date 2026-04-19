package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

type CatalogService struct {
	db      *gorm.DB
	storage FileStorage
}

func NewCatalogService(db *gorm.DB, storage FileStorage) *CatalogService {
	return &CatalogService{db: db, storage: storage}
}

// CatalogSearchResult wraps a catalog product with its accepted images.
type CatalogSearchResult struct {
	models.CatalogProduct
	Images []models.CatalogImage `json:"images,omitempty"`
}

// FindOrCreateCatalogProduct looks up by barcode first, then by name+brand.
// Creates with source="user" if not found.
func (s *CatalogService) FindOrCreateCatalogProduct(barcode, name, brand, presentation, content, category string) (*models.CatalogProduct, error) {
	var cp models.CatalogProduct

	// Try barcode first
	if barcode != "" {
		if err := s.db.Where("LOWER(barcode) = LOWER(?)", barcode).First(&cp).Error; err == nil {
			return &cp, nil
		}
	}

	// Try name+brand match
	if err := s.db.Where("LOWER(name) = LOWER(?) AND LOWER(brand) = LOWER(?)", name, brand).First(&cp).Error; err == nil {
		return &cp, nil
	}

	// Create new
	cp = models.CatalogProduct{
		Name:           name,
		NormalizedName: name,
		Brand:          brand,
		Barcode:        barcode,
		SKU:            barcode,
		Presentation:   presentation,
		Content:        content,
		Category:       category,
		IsAIEnhanced:   true,
		Source:         "user",
	}
	if err := s.db.Create(&cp).Error; err != nil {
		return nil, fmt.Errorf("failed to create catalog product: %w", err)
	}
	return &cp, nil
}

// CountAcceptedImages returns the number of accepted images for a catalog product.
func (s *CatalogService) CountAcceptedImages(catalogProductID string) (int64, error) {
	var count int64
	err := s.db.Model(&models.CatalogImage{}).
		Where("catalog_product_id = ? AND is_accepted = true", catalogProductID).
		Count(&count).Error
	return count, err
}

// CreatePendingImage is deprecated. Use CreateCatalogImage instead.
// Kept for backward compatibility during the transition; behaves identically
// to CreateCatalogImage (auto-accepted).
func (s *CatalogService) CreatePendingImage(catalogProductID, tenantID, imageURL, storageKey string) (*models.CatalogImage, error) {
	return s.CreateCatalogImage(catalogProductID, tenantID, imageURL, storageKey)
}

// CreateCatalogImage records a new catalog image as already accepted. We no
// longer use a pending state — an image that is linked to a merchant's
// product is always in use, and keeping a half-life "pending" state was the
// root cause of the cleanup/deletion bug. Community moderation, if added
// later, belongs on a separate workflow that operates on its own storage
// prefix (see CleanupExpiredImages).
func (s *CatalogService) CreateCatalogImage(catalogProductID, tenantID, imageURL, storageKey string) (*models.CatalogImage, error) {
	img := models.CatalogImage{
		CatalogProductID:  catalogProductID,
		ImageURL:          imageURL,
		StorageKey:        storageKey,
		CreatedByTenantID: tenantID,
		IsAccepted:        true,
	}
	if err := s.db.Create(&img).Error; err != nil {
		return nil, fmt.Errorf("create catalog image: %w", err)
	}
	return &img, nil
}

// AcceptImage marks a catalog image as accepted. Enforces max 3 per product.
func (s *CatalogService) AcceptImage(imageID string) error {
	var img models.CatalogImage
	if err := s.db.Where("id = ?", imageID).First(&img).Error; err != nil {
		return fmt.Errorf("imagen no encontrada: %w", err)
	}

	if img.IsAccepted {
		return nil // already accepted
	}

	count, err := s.CountAcceptedImages(img.CatalogProductID)
	if err != nil {
		return err
	}
	if count >= 3 {
		return nil // cap reached, silently skip
	}

	return s.db.Model(&img).Update("is_accepted", true).Error
}

// GetAcceptedImages returns up to 3 accepted images for a catalog product.
func (s *CatalogService) GetAcceptedImages(catalogProductID string) ([]models.CatalogImage, error) {
	var images []models.CatalogImage
	err := s.db.Where("catalog_product_id = ? AND is_accepted = true", catalogProductID).
		Order("created_at ASC").
		Limit(3).
		Find(&images).Error
	return images, err
}

// SearchCatalog searches with quality hierarchy:
// 1. AI-enhanced products with complete data (premium) — first
// 2. User-contributed products — second
// 3. OFF-cached products — third
// If local results < 3, falls back to OFF API via cacheSvc.
func (s *CatalogService) SearchCatalog(ctx context.Context, query string, limit int) ([]CatalogSearchResult, error) {
	q := "%" + strings.ToLower(query) + "%"

	var products []models.CatalogProduct
	err := s.db.WithContext(ctx).
		Where("LOWER(name) LIKE ? OR LOWER(brand) LIKE ? OR LOWER(barcode) LIKE ?", q, q, q).
		Order(`CASE
			WHEN source = 'user' AND is_ai_enhanced = true AND presentation != '' AND content != '' THEN 0
			WHEN source = 'user' THEN 1
			ELSE 2
		END, updated_at DESC`).
		Limit(limit).
		Find(&products).Error
	if err != nil {
		return nil, err
	}

	results := make([]CatalogSearchResult, 0, len(products))
	for _, p := range products {
		r := CatalogSearchResult{CatalogProduct: p}
		if p.Source == "user" {
			images, _ := s.GetAcceptedImages(p.ID)
			r.Images = images
			if r.ImageURL == "" && len(images) > 0 {
				r.ImageURL = images[0].ImageURL
			}
		}
		results = append(results, r)
	}

	return results, nil
}

// SearchCatalogWithFallback searches local first, then OFF if not enough results.
func (s *CatalogService) SearchCatalogWithFallback(ctx context.Context, query string, limit int, cacheSvc *CatalogCacheService) ([]CatalogSearchResult, error) {
	results, err := s.SearchCatalog(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	// If we have enough premium results, return them
	if len(results) >= 3 {
		return results, nil
	}

	// Fallback: fetch from OFF via cache service
	if cacheSvc != nil {
		offProducts, err := cacheSvc.SearchProducts(ctx, query, limit-len(results))
		if err == nil {
			// Deduplicate: don't add OFF results that match existing local names
			seen := make(map[string]bool)
			for _, r := range results {
				seen[strings.ToLower(r.Name)] = true
			}
			for _, op := range offProducts {
				if seen[strings.ToLower(op.Name)] {
					continue
				}
				seen[strings.ToLower(op.Name)] = true
				results = append(results, CatalogSearchResult{
					CatalogProduct: models.CatalogProduct{
						Name:     op.Name,
						Brand:    op.Brand,
						ImageURL: op.ImageURL,
						Barcode:  op.Barcode,
						Category: op.Category,
						Source:   "off",
					},
				})
				if len(results) >= limit {
					break
				}
			}
		}
	}

	return results, nil
}

// PromoteInUseImages marks as accepted every catalog image that is already
// referenced by a product's photo_url/image_url. This is a safety net so a
// stale cleanup run can never delete a bucket file that a merchant's live
// product depends on.
func (s *CatalogService) PromoteInUseImages() (int64, error) {
	res := s.db.Exec(`
		UPDATE catalog_images ci
		SET is_accepted = true, updated_at = NOW()
		FROM products p
		WHERE ci.is_accepted = false
		  AND (p.photo_url = ci.image_url OR p.image_url = ci.image_url)
	`)
	if res.Error != nil {
		return 0, fmt.Errorf("promote in-use images: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// catalogPendingPrefix is the ONLY storage prefix whose objects this service
// is allowed to delete. Merchant-owned product photos live under "products/"
// and must never be removed by a background job. This prefix is currently
// unused (we no longer create pending rows), so CleanupExpiredImages is
// effectively a no-op guard that stays wired in case a future community-
// moderation workflow starts writing into "catalog-pending/".
const catalogPendingPrefix = "catalog-pending/"

// CleanupExpiredImages deletes catalog_images rows that expired without being
// accepted. It has three layers of guard:
//  1. The row's storage_key must live under catalog-pending/. Anything else
//     is merchant-owned and off-limits.
//  2. No product row may still reference the image_url. If one does, the
//     row is promoted to accepted and the bucket file is left alone.
//  3. Only then do we delete the bucket file and the row.
//
// With current code paths no row is ever created with is_accepted=false, so
// this function reports zero work on every run. The guards remain as hard
// defense in depth for future changes.
func (s *CatalogService) CleanupExpiredImages(ctx context.Context, maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)

	var images []models.CatalogImage
	if err := s.db.Where("is_accepted = false AND created_at < ?", cutoff).Find(&images).Error; err != nil {
		return fmt.Errorf("load expired images: %w", err)
	}

	promoted := 0
	deleted := 0
	refused := 0
	for _, img := range images {
		var inUse int64
		if err := s.db.Model(&models.Product{}).
			Where("photo_url = ? OR image_url = ?", img.ImageURL, img.ImageURL).
			Count(&inUse).Error; err != nil {
			log.Printf("[CATALOG] cleanup: in-use check failed for %s: %v", img.StorageKey, err)
			continue
		}
		if inUse > 0 {
			if err := s.db.Model(&img).Update("is_accepted", true).Error; err != nil {
				log.Printf("[CATALOG] cleanup: failed to promote in-use image %s: %v", img.StorageKey, err)
				continue
			}
			promoted++
			continue
		}
		// Prefix guard: never delete bucket objects outside the catalog-pending
		// namespace. If we ever see a row here with a merchant-owned key,
		// refuse and log loudly so the mismatch shows up in observability.
		if img.StorageKey != "" && !strings.HasPrefix(img.StorageKey, catalogPendingPrefix) {
			log.Printf("[CATALOG] cleanup REFUSED: storage_key %q is outside %q — skipping delete", img.StorageKey, catalogPendingPrefix)
			refused++
			continue
		}
		if s.storage != nil && img.StorageKey != "" {
			if err := s.storage.Delete(ctx, "product-photos", img.StorageKey); err != nil {
				log.Printf("[CATALOG] warning: failed to delete %s from bucket: %v", img.StorageKey, err)
				continue
			}
		}
		if err := s.db.Delete(&img).Error; err != nil {
			log.Printf("[CATALOG] cleanup: failed to delete row %s: %v", img.ID, err)
			continue
		}
		deleted++
	}

	if promoted > 0 || deleted > 0 || refused > 0 {
		log.Printf("[CATALOG] cleanup: promoted %d in-use, deleted %d catalog-pending, refused %d merchant-owned", promoted, deleted, refused)
	}
	return nil
}

// StartCleanupTicker runs image cleanup every hour. TTL is 7 days to give
// merchants time to finish editing without losing their uploaded/generated
// photos. The in-use check in CleanupExpiredImages is the hard guarantee.
func (s *CatalogService) StartCleanupTicker(ctx context.Context) {
	// Immediate startup safety net: promote any live product image that is
	// still marked pending in catalog_images.
	if n, err := s.PromoteInUseImages(); err != nil {
		log.Printf("[CATALOG] startup promote failed: %v", err)
	} else if n > 0 {
		log.Printf("[CATALOG] startup: promoted %d in-use catalog images", n)
	}

	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.CleanupExpiredImages(ctx, 7*24*time.Hour); err != nil {
					log.Printf("[CATALOG] cleanup error: %v", err)
				}
			}
		}
	}()
	log.Println("[SVC] Catalog image cleanup ticker started (every 1h, TTL 7 days, in-use protected)")
}
