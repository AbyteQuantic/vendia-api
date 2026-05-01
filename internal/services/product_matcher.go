package services

import (
	"crypto/sha256"
	"fmt"
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// MatchCandidate represents a product match with confidence score.
type MatchCandidate struct {
	ProductID    string  `json:"product_id"`
	ProductName  string  `json:"product_name"`
	Barcode      string  `json:"barcode,omitempty"`
	Presentation string  `json:"presentation,omitempty"`
	Content      string  `json:"content,omitempty"`
	Stock        int     `json:"stock"`
	Price        float64 `json:"price"`
	Confidence   float64 `json:"confidence"`
	MatchMethod  string  `json:"match_method"` // "barcode", "normalized", "fuzzy"
}

// MatchProductRequest is a single product to match.
type MatchProductRequest struct {
	Name         string `json:"name"`
	Barcode      string `json:"barcode,omitempty"`
	Presentation string `json:"presentation,omitempty"`
	Content      string `json:"content,omitempty"`
}

// MatchProducts runs a 3-level matching algorithm against the tenant catalog.
//
//  1. Barcode exact match → confidence 1.0
//  2. Normalized name+presentation+content match → confidence 0.9
//  3. pg_trgm fuzzy name → confidence = similarity score
func MatchProducts(db *gorm.DB, tenantID string, items []MatchProductRequest) [][]MatchCandidate {
	results := make([][]MatchCandidate, len(items))

	for i, item := range items {
		var candidates []MatchCandidate

		// Level 1: Barcode exact
		if item.Barcode != "" {
			var product models.Product
			if err := db.Where("barcode = ? AND tenant_id = ? AND is_available = true",
				item.Barcode, tenantID).First(&product).Error; err == nil {
				candidates = append(candidates, MatchCandidate{
					ProductID:    product.ID,
					ProductName:  product.Name,
					Barcode:      product.Barcode,
					Presentation: product.Presentation,
					Content:      product.Content,
					Stock:        product.Stock,
					Price:        product.Price,
					Confidence:   1.0,
					MatchMethod:  "barcode",
				})
				results[i] = candidates
				continue
			}
		}

		// Level 2: Normalized name+presentation+content exact
		normName := NormalizeText(item.Name)
		normPres := NormalizeText(item.Presentation)
		normContent := NormalizeText(item.Content)
		normKey := normName + "|" + normPres + "|" + normContent

		var products []models.Product
		db.Where("tenant_id = ? AND is_available = true", tenantID).
			Find(&products)

		for _, p := range products {
			pKey := NormalizeText(p.Name) + "|" + NormalizeText(p.Presentation) + "|" + NormalizeText(p.Content)
			if pKey == normKey {
				candidates = append(candidates, MatchCandidate{
					ProductID:    p.ID,
					ProductName:  p.Name,
					Barcode:      p.Barcode,
					Presentation: p.Presentation,
					Content:      p.Content,
					Stock:        p.Stock,
					Price:        p.Price,
					Confidence:   0.9,
					MatchMethod:  "normalized",
				})
			}
		}

		if len(candidates) > 0 {
			results[i] = candidates
			continue
		}

		// Level 3: pg_trgm fuzzy
		if normName != "" {
			var fuzzyResults []struct {
				models.Product
				Similarity float64
			}
			db.Raw(`
				SELECT p.*, similarity(LOWER(p.name), ?) AS similarity
				FROM products p
				WHERE p.tenant_id = ?
				  AND p.is_available = true
				  AND p.deleted_at IS NULL
				  AND similarity(LOWER(p.name), ?) > 0.3
				ORDER BY similarity DESC
				LIMIT 5
			`, normName, tenantID, normName).Scan(&fuzzyResults)

			for _, r := range fuzzyResults {
				candidates = append(candidates, MatchCandidate{
					ProductID:    r.Product.ID,
					ProductName:  r.Product.Name,
					Barcode:      r.Product.Barcode,
					Presentation: r.Product.Presentation,
					Content:      r.Product.Content,
					Stock:        r.Product.Stock,
					Price:        r.Product.Price,
					Confidence:   r.Similarity,
					MatchMethod:  "fuzzy",
				})
			}
		}

		results[i] = candidates
	}

	return results
}

// ImageIdempotencyKey generates a SHA256 key from the first 2KB of image data.
func ImageIdempotencyKey(data []byte) string {
	limit := 2048
	if len(data) < limit {
		limit = len(data)
	}
	h := sha256.Sum256(data[:limit])
	return fmt.Sprintf("img:%x", h)
}
