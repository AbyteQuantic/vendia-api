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

// CreatePendingImage creates an unaccepted catalog image record.
func (s *CatalogService) CreatePendingImage(catalogProductID, tenantID, imageURL, storageKey string) (*models.CatalogImage, error) {
	img := models.CatalogImage{
		CatalogProductID:  catalogProductID,
		ImageURL:          imageURL,
		StorageKey:        storageKey,
		CreatedByTenantID: tenantID,
		IsAccepted:        false,
	}
	if err := s.db.Create(&img).Error; err != nil {
		return nil, fmt.Errorf("failed to create catalog image: %w", err)
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

// CleanupExpiredImages deletes unaccepted images older than maxAge from bucket and DB.
func (s *CatalogService) CleanupExpiredImages(ctx context.Context, maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)

	var images []models.CatalogImage
	if err := s.db.Where("is_accepted = false AND created_at < ?", cutoff).Find(&images).Error; err != nil {
		return err
	}

	for _, img := range images {
		if s.storage != nil && img.StorageKey != "" {
			if err := s.storage.Delete(ctx, "product-photos", img.StorageKey); err != nil {
				log.Printf("[CATALOG] warning: failed to delete image from bucket: %v", err)
			}
		}
		s.db.Delete(&img)
	}

	if len(images) > 0 {
		log.Printf("[CATALOG] cleaned up %d expired images", len(images))
	}
	return nil
}

// StartCleanupTicker runs image cleanup every hour.
func (s *CatalogService) StartCleanupTicker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.CleanupExpiredImages(ctx, 30*time.Minute); err != nil {
					log.Printf("[CATALOG] cleanup error: %v", err)
				}
			}
		}
	}()
	log.Println("[SVC] Catalog image cleanup ticker started (every 1h, TTL 30min)")
}
