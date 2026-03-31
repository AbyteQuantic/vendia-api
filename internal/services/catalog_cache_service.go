package services

import (
	"context"
	"log"
	"strings"
	"time"
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// CatalogCacheService provides cached product search backed by PostgreSQL.
// On search: returns DB results first; if fewer than `limit`, fetches from
// Open Food Facts, caches the new products, and returns the combined set.
// A daily goroutine refreshes stale entries.
type CatalogCacheService struct {
	db     *gorm.DB
	offSvc *OpenFoodFactsService
	maxAge time.Duration
}

func NewCatalogCacheService(db *gorm.DB, offSvc *OpenFoodFactsService) *CatalogCacheService {
	return &CatalogCacheService{
		db:     db,
		offSvc: offSvc,
		maxAge: 24 * time.Hour,
	}
}

// SearchProducts searches the local cache first; if results are insufficient,
// queries OFF, caches the results, and returns the merged list.
func (s *CatalogCacheService) SearchProducts(ctx context.Context, query string, limit int) ([]OFFProduct, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}

	// 1. Search local cache
	var cached []models.CatalogProduct
	pattern := "%" + strings.ToLower(query) + "%"
	err := s.db.WithContext(ctx).
		Where("LOWER(name) LIKE ? OR LOWER(brand) LIKE ?", pattern, pattern).
		Order("fetched_at DESC").
		Limit(limit).
		Find(&cached).Error
	if err != nil {
		return nil, err
	}

	// 2. If we have enough results, return them
	if len(cached) >= limit {
		return toCatalogResults(cached[:limit]), nil
	}

	// 3. Fetch from OFF to fill the gap
	offProducts, err := s.offSvc.SearchProducts(ctx, query, limit)
	if err != nil {
		// If OFF fails but we have partial cache, return what we have
		if len(cached) > 0 {
			return toCatalogResults(cached), nil
		}
		return nil, err
	}

	// 4. Cache new products (upsert by name+brand)
	s.cacheProducts(offProducts)

	// 5. Merge: cached + OFF, deduplicated, up to limit
	return s.mergeResults(cached, offProducts, limit), nil
}

// cacheProducts upserts OFF results into the catalog_products table.
// Uses find-or-create pattern because the unique index uses LOWER().
func (s *CatalogCacheService) cacheProducts(products []OFFProduct) {
	now := time.Now()
	for _, p := range products {
		if p.Name == "" {
			continue
		}
		var existing models.CatalogProduct
		err := s.db.Where("LOWER(name) = LOWER(?) AND LOWER(brand) = LOWER(?)", p.Name, p.Brand).
			First(&existing).Error
		if err == nil {
			// Update existing entry
			s.db.Model(&existing).Updates(map[string]any{
				"image_url":  p.ImageURL,
				"fetched_at": now,
			})
		} else {
			// Create new entry
			s.db.Create(&models.CatalogProduct{
				Name:      p.Name,
				Brand:     p.Brand,
				ImageURL:  p.ImageURL,
				Barcode:   p.Barcode,
				Category:  p.Category,
				FetchedAt: now,
			})
		}
	}
}

func (s *CatalogCacheService) mergeResults(cached []models.CatalogProduct, off []OFFProduct, limit int) []OFFProduct {
	seen := make(map[string]bool)
	var results []OFFProduct

	for _, c := range cached {
		key := strings.ToLower(c.Name + "|" + c.Brand)
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, OFFProduct{
			Name:     c.Name,
			Brand:    c.Brand,
			ImageURL: c.ImageURL,
			Barcode:  c.Barcode,
			Category: c.Category,
		})
	}

	for _, p := range off {
		if len(results) >= limit {
			break
		}
		key := strings.ToLower(p.Name + "|" + p.Brand)
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, p)
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// StartDailyRefresh launches a background goroutine that refreshes stale
// catalog entries every 24 hours. It also pre-populates common Colombian
// product searches on first run.
func (s *CatalogCacheService) StartDailyRefresh(ctx context.Context) {
	go func() {
		// Pre-seed on startup if catalog is empty
		var count int64
		s.db.Model(&models.CatalogProduct{}).Count(&count)
		if count == 0 {
			log.Println("[CATALOG] seeding product catalog from Open Food Facts...")
			s.seedCommonProducts(ctx)
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("[CATALOG] daily refresh stopped")
				return
			case <-ticker.C:
				log.Println("[CATALOG] refreshing stale catalog entries...")
				s.refreshStaleEntries(ctx)
			}
		}
	}()
}

// refreshStaleEntries re-fetches products older than maxAge by re-querying
// OFF with the product names as search terms.
func (s *CatalogCacheService) refreshStaleEntries(ctx context.Context) {
	staleThreshold := time.Now().Add(-s.maxAge)

	// Get distinct names from stale entries to use as refresh queries
	var staleNames []string
	s.db.Model(&models.CatalogProduct{}).
		Where("fetched_at < ?", staleThreshold).
		Distinct("name").
		Limit(50).
		Pluck("name", &staleNames)

	for _, name := range staleNames {
		select {
		case <-ctx.Done():
			return
		default:
		}

		products, err := s.offSvc.SearchProducts(ctx, name, 5)
		if err != nil {
			continue
		}
		s.cacheProducts(products)
		// Be polite to OFF API
		time.Sleep(500 * time.Millisecond)
	}

	// Delete entries that are very old (>7 days) and weren't refreshed
	weekAgo := time.Now().Add(-7 * 24 * time.Hour)
	s.db.Where("fetched_at < ?", weekAgo).Delete(&models.CatalogProduct{})

	log.Printf("[CATALOG] refresh complete, processed %d queries", len(staleNames))
}

// seedCommonProducts pre-populates the cache with searches typical for
// Colombian tiendas de barrio.
func (s *CatalogCacheService) seedCommonProducts(ctx context.Context) {
	queries := []string{
		"coca cola", "pepsi", "agua", "leche", "pan",
		"arroz", "aceite", "azucar", "cafe", "chocolate",
		"galletas", "jabon", "detergente", "cerveza", "aguardiente",
		"atun", "sardinas", "pasta", "harina", "sal",
		"jugo", "yogurt", "queso", "huevos", "mantequilla",
	}

	for _, q := range queries {
		select {
		case <-ctx.Done():
			return
		default:
		}

		products, err := s.offSvc.SearchProducts(ctx, q, 10)
		if err != nil {
			log.Printf("[CATALOG] seed failed for %q: %v", q, err)
			continue
		}
		s.cacheProducts(products)
		time.Sleep(300 * time.Millisecond) // rate limit
	}
	log.Printf("[CATALOG] seeded catalog with %d queries", len(queries))
}

func toCatalogResults(cached []models.CatalogProduct) []OFFProduct {
	results := make([]OFFProduct, 0, len(cached))
	for _, c := range cached {
		results = append(results, OFFProduct{
			Name:     c.Name,
			Brand:    c.Brand,
			ImageURL: c.ImageURL,
			Barcode:  c.Barcode,
			Category: c.Category,
		})
	}
	return results
}
