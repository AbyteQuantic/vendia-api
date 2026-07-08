// Spec: specs/027-importador-inventario/spec.md
package handlers

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"vendia-backend/internal/middleware"
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
	// FuzzyMatches (Spec 099 FR-09): rows that didn't match anything
	// exactly (no barcode, no exact normalized name) but resemble an
	// existing product closely enough to be worth a second look — purely
	// informational, never blocks or merges the row. The importer has no
	// per-row review UI (unlike voice/factura), so silently auto-merging
	// an approximate match here would violate "coincidencia aproximada
	// nunca se aplica sin confirmación" (spec §7) — this is the
	// confirmation-free alternative: still create the row, but tell the
	// tendero afterward so they can review it in Mi Inventario.
	FuzzyMatches []productImportFuzzyMatch `json:"fuzzy_matches,omitempty"`
}

// productImportFuzzyMatch is one "this looks like a product you
// already have" heads-up surfaced after import.
type productImportFuzzyMatch struct {
	RowIndex      int     `json:"row_index"`
	RowName       string  `json:"row_name"`
	CandidateID   string  `json:"candidate_id"`
	CandidateName string  `json:"candidate_name"`
	Similarity    float64 `json:"similarity"`
}

// buildFuzzyMatchEntry reduces a candidate list to the single
// highest-confidence fuzzy match worth surfacing, or nil when there are
// no candidates. Extracted as a pure function (no DB/SQL inside) so it's
// directly unit-testable — the actual pg_trgm similarity() query has no
// SQLite equivalent to test against (same gotcha as MatchProducts'
// existing fuzzy level), verified in production per Art. XII.
func buildFuzzyMatchEntry(rowIndex int, rowName string, candidates []services.MatchCandidate) *productImportFuzzyMatch {
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.Confidence > best.Confidence {
			best = c
		}
	}
	return &productImportFuzzyMatch{
		RowIndex: rowIndex, RowName: rowName,
		CandidateID: best.ProductID, CandidateName: best.ProductName,
		Similarity: best.Confidence,
	}
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

		userID := middleware.GetUserIDPtr(c)
		for i, row := range req.Rows {
			fail := processProductImportRow(db, tenantID, userID, i, row, &result)
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
//
// Every branch (create / update-by-barcode / update-by-name) runs inside a
// transaction that also writes the matching kardex movement (Art. VII):
// a stock change that lands in `products` without a paired
// `inventory_movements` row breaks the audit trail the same way it would
// for the manual create/edit flow in products.go — CSV import is just
// another entry channel for the same invariant.
func processProductImportRow(db *gorm.DB, tenantID string, userID *string, idx int, row productImportRow, result *productImportResult) *importFailedRow {
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
			oldStock := existing.Stock
			updates := buildProductUpdateMap(row, price, stock, minStock, purchasePrice)
			txErr := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
					return err
				}
				return logImportStockAdjustment(tx, tenantID, userID, existing.ID, existing.Name, oldStock, stock)
			})
			if txErr != nil {
				return &importFailedRow{RowIndex: idx, Reason: "error al actualizar: " + txErr.Error()}
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
					// Spec 100 / D1 — esta rama escribe row.Barcode sobre el
					// producto que matcheó por NOMBRE: si el código ya es de
					// otro producto vivo, la fila falla con razón en español
					// (nunca el error crudo de Postgres al tendero).
					if failed := importBarcodeConflict(db, tenantID, idx, row.Barcode, p.ID); failed != nil {
						return failed
					}
					oldStock := p.Stock
					updates := buildProductUpdateMap(row, price, stock, minStock, purchasePrice)
					txErr := db.Transaction(func(tx *gorm.DB) error {
						if err := tx.Model(&p).Updates(updates).Error; err != nil {
							return err
						}
						return logImportStockAdjustment(tx, tenantID, userID, p.ID, p.Name, oldStock, stock)
					})
					if txErr != nil {
						// Carrera: el índice único detuvo la escritura → misma
						// razón limpia que el pre-check.
						if failed := importBarcodeFailedRow(db, tenantID, idx, row.Barcode, p.ID, txErr); failed != nil {
							return failed
						}
						return &importFailedRow{RowIndex: idx, Reason: "error al actualizar: " + txErr.Error()}
					}
					result.Updated++
					return nil
				}
			}
		}
	}

	// ── Fuzzy match heads-up (Spec 099 FR-09) ───────────────────────────────
	// No exact match (barcode or normalized name) was found above — before
	// inserting, check for an approximate resemblance worth flagging.
	// Never blocks or changes the INSERT below; purely informational.
	fuzzyCandidates := services.MatchProducts(db, tenantID, []services.MatchProductRequest{{
		Name: row.Name, Barcode: row.Barcode, Presentation: row.Presentation, Content: row.Content,
	}}, "")[0]
	if entry := buildFuzzyMatchEntry(idx, row.Name, fuzzyCandidates); entry != nil {
		result.FuzzyMatches = append(result.FuzzyMatches, *entry)
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

	// Create + kardex movement run inside one transaction, mirroring
	// CreateProduct in products.go: a kardex write failure must roll back
	// the insert instead of leaving a product row with stock but no audit
	// trail (Art. VII).
	txErr := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&product).Error; err != nil {
			return err
		}
		if product.Stock <= 0 {
			return nil
		}
		// Same StockBeforeOverride/StockAfterOverride pattern as
		// CreateProduct: the row already carries the starting stock, so a
		// self-read would fabricate stock_before=stock_inicial /
		// stock_after=2×stock_inicial instead of 0 → stock_inicial.
		zero := float64(0)
		initial := float64(product.Stock)
		return services.LogInventoryMovement(tx, services.MovementParams{
			TenantID:            tenantID,
			ProductID:           product.ID,
			ProductName:         product.Name,
			MovementType:        models.MovementInitialStock,
			Quantity:            product.Stock,
			UserID:              userID,
			Notes:               "alta por importación masiva (CSV)",
			StockBeforeOverride: &zero,
			StockAfterOverride:  &initial,
		})
	})
	if txErr != nil {
		// Spec 100 / D1 — carrera en el INSERT: el barcode quedó libre en el
		// dedup de arriba pero otro request lo ganó antes del Create.
		if failed := importBarcodeFailedRow(db, tenantID, idx, row.Barcode, product.ID, txErr); failed != nil {
			return failed
		}
		return &importFailedRow{RowIndex: idx, Reason: "error al crear: " + txErr.Error()}
	}
	result.Created++
	return nil
}

// importBarcodeConflict — Spec 100 / D1. Devuelve la fila fallida (razón en
// ESPAÑOL con el nombre del producto dueño, sin internals de la BD) cuando
// `barcode` ya pertenece a OTRO producto vivo del tenant; nil si el código
// está libre. excludeID = el producto que la fila está actualizando (su
// propio código no conflictúa).
func importBarcodeConflict(db *gorm.DB, tenantID string, idx int, barcode, excludeID string) *importFailedRow {
	owner := services.FindBarcodeOwner(db, tenantID, barcode, excludeID)
	if owner == nil {
		return nil
	}
	return &importFailedRow{
		RowIndex: idx,
		Reason:   "código de barras ya usado por " + owner.Name,
	}
}

// importBarcodeFailedRow mapea la violación del índice único de barcode
// (carrera: otro request ganó el código entre el pre-check y la escritura)
// a la MISMA razón limpia en español. Devuelve nil para cualquier otro
// error, para que el caller conserve su manejo. Sin esto, el error crudo de
// Postgres (inglés + constraint + SQLSTATE 23505) llegaba tal cual al
// tendero en la respuesta del importador.
func importBarcodeFailedRow(db *gorm.DB, tenantID string, idx int, barcode, excludeID string, txErr error) *importFailedRow {
	if !services.IsProductBarcodeUniqueViolation(txErr) {
		return nil
	}
	if failed := importBarcodeConflict(db, tenantID, idx, barcode, excludeID); failed != nil {
		return failed
	}
	// El dueño ya no es legible (p. ej. borrado tras ganar la carrera):
	// razón genérica igual de limpia, jamás el error crudo.
	return &importFailedRow{
		RowIndex: idx,
		Reason:   "código de barras ya usado por otro producto",
	}
}

// logImportStockAdjustment logs a manual_adjust kardex movement for an
// import row that updated an existing product's stock (barcode or
// name-fallback dedup branch). No-ops when the CSV row didn't actually
// change stock, so a re-import of the same file (idempotent re-run,
// FR test (f)) doesn't create noise movements with quantity 0.
//
// MovementManualAdjust is reused rather than inventing a new
// MovementType: a bulk CSV update is, from the kardex's point of view,
// the same kind of event as a tendero manually correcting the stock
// column on the edit screen — an out-of-band quantity correction with no
// PO/sale/recipe backing it. The row's ingestion_method='import' column
// already records which channel produced the correction; the movement
// type only needs to capture "this was a manual correction, not a sale
// or a scanned invoice".
func logImportStockAdjustment(tx *gorm.DB, tenantID string, userID *string, productID, productName string, oldStock, newStock int) error {
	if newStock == oldStock {
		return nil
	}
	before := float64(oldStock)
	after := float64(newStock)
	return services.LogInventoryMovement(tx, services.MovementParams{
		TenantID:            tenantID,
		ProductID:           productID,
		ProductName:         productName,
		MovementType:        models.MovementManualAdjust,
		Quantity:            newStock - oldStock,
		UserID:              userID,
		Notes:               "ajuste por importación masiva (CSV)",
		StockBeforeOverride: &before,
		StockAfterOverride:  &after,
	})
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
