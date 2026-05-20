// Spec: specs/027-importador-inventario/spec.md
package handlers

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// productImportRow represents one row in the products import request body.
// Only allow-listed fields are accepted. Internal fields (tenant_id, id,
// timestamps, ingestion_method, is_ai_enhanced, photo_url, image_url,
// branch_id, is_recipe, recipe_id, etc.) are absent by design (FR-05, FR-09).
type productImportRow struct {
	Name          string `json:"name"`
	Price         string `json:"price"`
	Barcode       string `json:"barcode"`
	PurchasePrice string `json:"purchase_price"`
	Stock         string `json:"stock"`
	MinStock      string `json:"min_stock"`
	Category      string `json:"category"`
	Emoji         string `json:"emoji"`
	Unit          string `json:"unit"`
	Presentation  string `json:"presentation"`
	Content       string `json:"content"`
	ExpiryDate    string `json:"expiry_date"`
}

type productImportRequest struct {
	Rows          []productImportRow `json:"rows"`
	DedupStrategy string             `json:"dedup_strategy"`
}

// productImportResult mirrors the importResult shape of customers_import.go
// for a consistent API response envelope across all import endpoints.
type productImportResult struct {
	Created int               `json:"created"`
	Updated int               `json:"updated"`
	Skipped int               `json:"skipped"`
	Failed  []importFailedRow `json:"failed"`
}

// ImportProducts handles POST /api/v1/products/import.
//
// Body: { rows: [...], dedup_strategy: "merge_by_barcode_then_name" }
//   - max 100 rows per request (chunking is handled client-side).
//   - dedup: barcode exact match → UPDATE; name normalized fallback → UPDATE;
//     no match → INSERT with ingestion_method='import', is_ai_enhanced=false.
//
// God-mode: super_admin + X-Tenant-Override header → operates on that tenant.
func ImportProducts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// ── 1. Resolve tenant ID ─────────────────────────────────────────────
		tenantID, ok := resolveTenantID(c)
		if !ok {
			return
		}

		// ── 2. Parse and validate request body ───────────────────────────────
		var req productImportRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body inválido: " + err.Error()})
			return
		}

		if req.Rows == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el campo 'rows' es requerido"})
			return
		}

		if len(req.Rows) > maxImportRows {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "máximo 100 filas por solicitud — divide el archivo en chunks",
			})
			return
		}

		if req.DedupStrategy != "merge_by_barcode_then_name" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "dedup_strategy inválido: solo se acepta 'merge_by_barcode_then_name'",
			})
			return
		}

		// ── 3. Process each row ──────────────────────────────────────────────
		result := productImportResult{
			Failed: []importFailedRow{},
		}

		for i, row := range req.Rows {
			fail := processProductImportRow(db, tenantID, i, row, &result)
			if fail != nil {
				result.Failed = append(result.Failed, *fail)
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}

// processProductImportRow sanitizes, validates, and upserts one product row.
// Returns a *importFailedRow when the row must be counted as failed, nil
// on success. Updates result.Created / result.Updated in place.
func processProductImportRow(db *gorm.DB, tenantID string, idx int, row productImportRow, result *productImportResult) *importFailedRow {
	// ── Sanitize ─────────────────────────────────────────────────────────
	row.Name = normalizeWhitespace(row.Name)
	row.Barcode = strings.TrimSpace(row.Barcode)
	row.Category = strings.TrimSpace(row.Category)
	row.Emoji = strings.TrimSpace(row.Emoji)
	row.Unit = strings.TrimSpace(row.Unit)
	row.Presentation = strings.TrimSpace(row.Presentation)
	row.Content = strings.TrimSpace(row.Content)
	row.ExpiryDate = strings.TrimSpace(row.ExpiryDate)

	// ── Validate name ─────────────────────────────────────────────────────
	if row.Name == "" {
		return &importFailedRow{RowIndex: idx, Reason: "nombre vacío"}
	}

	// ── Normalize price (FR-10) ───────────────────────────────────────────
	priceStr := strings.TrimSpace(row.Price)
	price, err := services.NormalizePriceCOP(priceStr)
	if err != nil {
		return &importFailedRow{RowIndex: idx, Reason: "precio inválido: " + err.Error()}
	}

	// ── Parse stock (FR-11): decimal rounds to int; negative → failed ─────
	stock := 0
	if s := strings.TrimSpace(row.Stock); s != "" {
		stockF, parseErr := strconv.ParseFloat(s, 64)
		if parseErr != nil {
			return &importFailedRow{RowIndex: idx, Reason: "stock inválido: '" + s + "' no es un número"}
		}
		if stockF < 0 {
			return &importFailedRow{RowIndex: idx, Reason: "stock negativo no permitido"}
		}
		stock = int(math.Round(stockF))
	}

	// ── Parse min_stock ───────────────────────────────────────────────────
	minStock := 0
	if s := strings.TrimSpace(row.MinStock); s != "" {
		minStockF, parseErr := strconv.ParseFloat(s, 64)
		if parseErr == nil && minStockF >= 0 {
			minStock = int(math.Round(minStockF))
		}
	}

	// ── Parse purchase_price ──────────────────────────────────────────────
	purchasePrice := 0.0
	if s := strings.TrimSpace(row.PurchasePrice); s != "" {
		if pp, ppErr := services.NormalizePriceCOP(s); ppErr == nil {
			purchasePrice = pp
		}
	}

	// ── Dedup: barcode first, then normalized name ─────────────────────────
	if row.Barcode != "" {
		var existing models.Product
		err := db.Where("tenant_id = ? AND barcode = ? AND deleted_at IS NULL", tenantID, row.Barcode).
			First(&existing).Error
		if err == nil {
			// ── UPDATE (barcode match) ─────────────────────────────────
			updates := buildProductUpdateMap(row, price, stock, minStock, purchasePrice)
			if dbErr := db.Model(&existing).Updates(updates).Error; dbErr != nil {
				return &importFailedRow{RowIndex: idx, Reason: "error al actualizar: " + dbErr.Error()}
			}
			result.Updated++
			return nil
		}
		// ErrRecordNotFound → fall through to name dedup
	}

	// ── Name fallback dedup ───────────────────────────────────────────────
	normalizedName := services.NormalizeText(row.Name)
	var existingByName models.Product
	err = db.Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Find(&existingByName).Error
	if err == nil && existingByName.ID != "" {
		// We need to find by scanning (SQLite doesn't have unaccent).
		// Build the match in-memory for portability (tests use SQLite;
		// production Postgres could use lower(unaccent(name)) index).
		var products []models.Product
		if dbErr := db.Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
			Find(&products).Error; dbErr == nil {
			for _, p := range products {
				if services.NormalizeText(p.Name) == normalizedName {
					updates := buildProductUpdateMap(row, price, stock, minStock, purchasePrice)
					if dbErr := db.Model(&p).Updates(updates).Error; dbErr != nil {
						return &importFailedRow{RowIndex: idx, Reason: "error al actualizar: " + dbErr.Error()}
					}
					result.Updated++
					return nil
				}
			}
		}
	}

	// ── INSERT ────────────────────────────────────────────────────────────
	// FR-09: always set ingestion_method='import', is_ai_enhanced=false.
	product := models.Product{
		TenantID:        tenantID,
		Name:            row.Name,
		Price:           price,
		PurchasePrice:   purchasePrice,
		Stock:           stock,
		MinStock:        minStock,
		Barcode:         row.Barcode,
		Category:        row.Category,
		Emoji:           row.Emoji,
		Unit:            unitOrDefault(row.Unit),
		Presentation:    row.Presentation,
		Content:         row.Content,
		IngestionMethod: "import",
		IsAIEnhanced:    false,
	}
	if row.ExpiryDate != "" {
		product.ExpiryDate = &row.ExpiryDate
	}

	if dbErr := db.Create(&product).Error; dbErr != nil {
		return &importFailedRow{RowIndex: idx, Reason: "error al crear: " + dbErr.Error()}
	}
	result.Created++
	return nil
}

// buildProductUpdateMap constructs the map of fields to update for an existing
// product. Protected fields are intentionally excluded (FR-08, spec §7):
//   - last_order_at, is_ai_enhanced, photo_url, image_url, created_at,
//     branch_id, recipe_id, is_recipe.
//
// Only non-empty incoming fields are included so that an empty column in the
// import file does NOT overwrite existing data.
func buildProductUpdateMap(row productImportRow, price float64, stock, minStock int, purchasePrice float64) map[string]any {
	updates := map[string]any{
		"price": price,
		"stock": stock,
	}

	if row.Name != "" {
		updates["name"] = row.Name
	}
	if purchasePrice > 0 {
		updates["purchase_price"] = purchasePrice
	}
	if minStock > 0 {
		updates["min_stock"] = minStock
	}
	if row.Barcode != "" {
		updates["barcode"] = row.Barcode
	}
	if row.Category != "" {
		updates["category"] = row.Category
	}
	if row.Emoji != "" {
		updates["emoji"] = row.Emoji
	}
	if row.Unit != "" {
		updates["unit"] = row.Unit
	}
	if row.Presentation != "" {
		updates["presentation"] = row.Presentation
	}
	if row.Content != "" {
		updates["content"] = row.Content
	}
	if row.ExpiryDate != "" {
		updates["expiry_date"] = row.ExpiryDate
	}

	return updates
}

// unitOrDefault returns the provided unit string or the model default ("unit")
// when the string is empty.
func unitOrDefault(unit string) string {
	if unit == "" {
		return "unit"
	}
	return unit
}
