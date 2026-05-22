// Spec: specs/031-cotizaciones/spec.md
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// defaultQuoteValidityDays is the fallback validity window when a create
// request omits valid_until (Spec §4 — vigencia default 15 días).
const defaultQuoteValidityDays = 15

// quoteItemInput is one line of a create/update quote request. A line is
// either a product line (ProductID set, must belong to the tenant) or a
// free line (ProductID empty — e.g. "Mano de obra"). Validation of the
// XOR and of cross-tenant ownership happens in the handler.
type quoteItemInput struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	Quantity  float64 `json:"quantity"`
	UnitPrice float64 `json:"unit_price"`
	Discount  float64 `json:"discount"`
}

// quoteWriteRequest is the shared body shape for POST and PATCH /quotes.
type quoteWriteRequest struct {
	ID            string           `json:"id"`
	CustomerID    string           `json:"customer_id"`
	Items         []quoteItemInput `json:"items"`
	DiscountTotal float64          `json:"discount_total"`
	TaxRate       float64          `json:"tax_rate"`
	ValidUntil    *time.Time       `json:"valid_until"`
	Note          string           `json:"note"`
}

// CreateQuote persists a new quote in `borrador` state, assigning an
// atomic folio and a public token (Spec F031 AC-03, AC-04).
// POST /api/v1/quotes
func CreateQuote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req quoteWriteRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID válido"})
			return
		}

		// Validate the customer + items up-front (cross-tenant ownership,
		// shape) so we never open a transaction on a guaranteed failure.
		if err := validateCustomerOwnership(db, tenantID, req.CustomerID); err != nil {
			c.JSON(err.status, gin.H{"error": err.msg})
			return
		}
		priceLines, items, vErr := buildQuoteItems(db, tenantID, req.Items)
		if vErr != nil {
			c.JSON(vErr.status, gin.H{"error": vErr.msg})
			return
		}

		validUntil := resolveValidUntil(req.ValidUntil)
		totals := services.ComputeQuoteTotals(priceLines, req.DiscountTotal, req.TaxRate)
		for i := range items {
			items[i].Subtotal = totals.LineSubtotals[i]
		}

		var quote models.Quote
		err := db.Transaction(func(tx *gorm.DB) error {
			// Folio year follows creation time, not valid_until — a quote
			// created in 2026 is COT-2026-NNNN even if it expires in 2027.
			folio, err := services.NextQuoteFolio(tx, tenantID, time.Now().Year())
			if err != nil {
				return err
			}

			quote = models.Quote{
				TenantID:      tenantID,
				CustomerID:    req.CustomerID,
				Folio:         folio,
				Status:        models.QuoteStatusDraft,
				ValidUntil:    validUntil,
				Note:          strings.TrimSpace(req.Note),
				DiscountTotal: req.DiscountTotal,
				TaxRate:       req.TaxRate,
				Subtotal:      totals.Subtotal,
				TaxAmount:     totals.TaxAmount,
				Total:         totals.Total,
				PublicToken:   uuid.NewString(),
				Items:         items,
			}
			if req.ID != "" {
				quote.ID = req.ID
			}
			return tx.Create(&quote).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear la cotización"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": loadQuote(db, tenantID, quote.ID)})
	}
}

// GetQuote returns one quote with its items + customer (Spec F031).
// GET /api/v1/quotes/:id
func GetQuote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		quote := loadQuote(db, tenantID, c.Param("id"))
		if quote == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": quote})
	}
}

// ListQuotes returns the tenant's quotes, paginated, with optional
// filters: status (exact), q (folio or customer name substring),
// from/to (created_at range). Spec F031 AC-12.
// GET /api/v1/quotes
func ListQuotes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		base := db.Model(&models.Quote{}).Where("quotes.tenant_id = ?", tenantID)

		if status := strings.TrimSpace(c.Query("status")); status != "" {
			if !models.IsValidQuoteStatus(status) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "estado de cotización inválido"})
				return
			}
			base = base.Where("quotes.status = ?", status)
		}
		if q := strings.TrimSpace(c.Query("q")); q != "" {
			like := "%" + strings.ToLower(q) + "%"
			base = base.
				Joins("LEFT JOIN customers ON customers.id = quotes.customer_id").
				Where("LOWER(quotes.folio) LIKE ? OR LOWER(customers.name) LIKE ?", like, like)
		}
		if from := strings.TrimSpace(c.Query("from")); from != "" {
			if ts, err := time.Parse("2006-01-02", from); err == nil {
				base = base.Where("quotes.created_at >= ?", ts)
			}
		}
		if to := strings.TrimSpace(c.Query("to")); to != "" {
			if ts, err := time.Parse("2006-01-02", to); err == nil {
				base = base.Where("quotes.created_at < ?", ts.AddDate(0, 0, 1))
			}
		}

		var total int64
		base.Count(&total)

		var quotes []models.Quote
		if err := base.
			Preload("Items").
			Preload("Customer").
			Order("quotes.created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&quotes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener cotizaciones"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(quotes, total, p))
	}
}

// UpdateQuote edits a quote. A `borrador` is overwritten in place. An
// `enviada` quote is versioned: a v2 row is created with a `-V2` folio
// suffix and the v1 row is marked `reemplazada` pointing at the v2
// (Spec F031 AC-11). Any other state → 400.
// PATCH /api/v1/quotes/:id
func UpdateQuote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var existing models.Quote
		if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
			First(&existing).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		if existing.Status != models.QuoteStatusDraft && existing.Status != models.QuoteStatusSent {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "solo se puede editar una cotización en borrador o enviada",
			})
			return
		}

		var req quoteWriteRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		customerID := req.CustomerID
		if customerID == "" {
			customerID = existing.CustomerID
		}
		if err := validateCustomerOwnership(db, tenantID, customerID); err != nil {
			c.JSON(err.status, gin.H{"error": err.msg})
			return
		}
		priceLines, items, vErr := buildQuoteItems(db, tenantID, req.Items)
		if vErr != nil {
			c.JSON(vErr.status, gin.H{"error": vErr.msg})
			return
		}

		validUntil := existing.ValidUntil
		if req.ValidUntil != nil {
			validUntil = req.ValidUntil.UTC()
		}
		totals := services.ComputeQuoteTotals(priceLines, req.DiscountTotal, req.TaxRate)
		for i := range items {
			items[i].Subtotal = totals.LineSubtotals[i]
		}

		// ── borrador: overwrite in place ────────────────────────────────
		if existing.Status == models.QuoteStatusDraft {
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("quote_id = ?", existing.ID).
					Delete(&models.QuoteItem{}).Error; err != nil {
					return err
				}
				for i := range items {
					items[i].QuoteID = existing.ID
				}
				updates := map[string]any{
					"customer_id":    customerID,
					"discount_total": req.DiscountTotal,
					"tax_rate":       req.TaxRate,
					"subtotal":       totals.Subtotal,
					"tax_amount":     totals.TaxAmount,
					"total":          totals.Total,
					"valid_until":    validUntil,
					"note":           strings.TrimSpace(req.Note),
				}
				if err := tx.Model(&models.Quote{}).
					Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
					return err
				}
				if len(items) > 0 {
					return tx.Create(&items).Error
				}
				return nil
			})
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar la cotización"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"data": loadQuote(db, tenantID, existing.ID)})
			return
		}

		// ── enviada: create v2, mark v1 reemplazada ─────────────────────
		var v2 models.Quote
		err := db.Transaction(func(tx *gorm.DB) error {
			v2 = models.Quote{
				TenantID:      tenantID,
				CustomerID:    customerID,
				Folio:         nextVersionFolio(existing.Folio),
				Status:        models.QuoteStatusDraft,
				ValidUntil:    validUntil,
				Note:          strings.TrimSpace(req.Note),
				DiscountTotal: req.DiscountTotal,
				TaxRate:       req.TaxRate,
				Subtotal:      totals.Subtotal,
				TaxAmount:     totals.TaxAmount,
				Total:         totals.Total,
				PublicToken:   uuid.NewString(),
				Items:         items,
			}
			if err := tx.Create(&v2).Error; err != nil {
				return err
			}
			// v1 → reemplazada, pointing at v2. FSM-checked.
			if !services.CanTransition(existing.Status, models.QuoteStatusReplaced) {
				return fmt.Errorf("transición inválida")
			}
			return tx.Model(&models.Quote{}).Where("id = ?", existing.ID).
				Updates(map[string]any{
					"status":         models.QuoteStatusReplaced,
					"replaced_by_id": v2.ID,
				}).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear la versión 2"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": loadQuote(db, tenantID, v2.ID)})
	}
}

// DeleteQuote soft-deletes a quote — only allowed while `borrador`.
// Approved/rejected/converted quotes are kept for audit (Spec plan §4).
// DELETE /api/v1/quotes/:id
func DeleteQuote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var quote models.Quote
		if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
			First(&quote).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}
		if quote.Status != models.QuoteStatusDraft {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "solo se puede eliminar una cotización en borrador",
			})
			return
		}
		if err := db.Delete(&quote).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo eliminar la cotización"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "cotización eliminada"})
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// validationError carries an HTTP status + Spanish message so the
// up-front validators can short-circuit a handler cleanly.
type validationError struct {
	status int
	msg    string
}

// validateCustomerOwnership checks that customerID is a valid UUID and
// the customer belongs to tenantID (Constitución Art. III + VI — a
// crafted payload must not attach a quote to a foreign customer).
func validateCustomerOwnership(db *gorm.DB, tenantID, customerID string) *validationError {
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return &validationError{http.StatusBadRequest, "el cliente es obligatorio"}
	}
	if !models.IsValidUUID(customerID) {
		return &validationError{http.StatusBadRequest, "customer_id debe ser un UUID válido"}
	}
	var count int64
	db.Model(&models.Customer{}).
		Where("id = ? AND tenant_id = ?", customerID, tenantID).Count(&count)
	if count == 0 {
		return &validationError{http.StatusBadRequest, "cliente no encontrado"}
	}
	return nil
}

// buildQuoteItems validates the request lines and returns the pricing
// projection + the persistable QuoteItem rows. A product line must
// reference a product owned by tenantID; a free line must carry a name.
func buildQuoteItems(db *gorm.DB, tenantID string, in []quoteItemInput) (
	[]services.QuotePriceLine, []models.QuoteItem, *validationError,
) {
	if len(in) == 0 {
		return nil, nil, &validationError{http.StatusBadRequest, "la cotización debe tener al menos un ítem"}
	}

	priceLines := make([]services.QuotePriceLine, 0, len(in))
	items := make([]models.QuoteItem, 0, len(in))

	for idx, line := range in {
		if line.Quantity <= 0 {
			return nil, nil, &validationError{http.StatusBadRequest,
				fmt.Sprintf("ítem %d: la cantidad debe ser mayor a 0", idx+1)}
		}
		if line.UnitPrice < 0 || line.Discount < 0 {
			return nil, nil, &validationError{http.StatusBadRequest,
				fmt.Sprintf("ítem %d: precio y descuento no pueden ser negativos", idx+1)}
		}

		item := models.QuoteItem{
			Name:      strings.TrimSpace(line.Name),
			Quantity:  line.Quantity,
			UnitPrice: line.UnitPrice,
			Discount:  line.Discount,
			SortOrder: idx,
		}

		productID := strings.TrimSpace(line.ProductID)
		if productID != "" {
			if !models.IsValidUUID(productID) {
				return nil, nil, &validationError{http.StatusBadRequest,
					fmt.Sprintf("ítem %d: product_id debe ser un UUID válido", idx+1)}
			}
			var product models.Product
			if err := db.Where("id = ? AND tenant_id = ?", productID, tenantID).
				First(&product).Error; err != nil {
				return nil, nil, &validationError{http.StatusBadRequest,
					fmt.Sprintf("ítem %d: producto no encontrado", idx+1)}
			}
			pid := product.ID
			item.ProductID = &pid
			if item.Name == "" {
				item.Name = product.Name
			}
		} else if item.Name == "" {
			return nil, nil, &validationError{http.StatusBadRequest,
				fmt.Sprintf("ítem %d: una línea libre requiere un nombre", idx+1)}
		}

		items = append(items, item)
		priceLines = append(priceLines, services.QuotePriceLine{
			Quantity:  line.Quantity,
			UnitPrice: line.UnitPrice,
			Discount:  line.Discount,
		})
	}
	return priceLines, items, nil
}

// resolveValidUntil returns the requested expiry or the default window
// (now + 15 days) when none was provided.
func resolveValidUntil(v *time.Time) time.Time {
	if v != nil {
		return v.UTC()
	}
	return time.Now().UTC().AddDate(0, 0, defaultQuoteValidityDays)
}

// nextVersionFolio derives the next version folio. COT-2026-0001 →
// COT-2026-0001-V2; COT-2026-0001-V2 → COT-2026-0001-V3.
func nextVersionFolio(folio string) string {
	base := folio
	version := 2
	if idx := strings.LastIndex(folio, "-V"); idx != -1 {
		var n int
		if _, err := fmt.Sscanf(folio[idx+2:], "%d", &n); err == nil && n > 0 {
			base = folio[:idx]
			version = n + 1
		}
	}
	return fmt.Sprintf("%s-V%d", base, version)
}

// loadQuote fetches a single quote (with items + customer), tenant-scoped.
// Returns nil when not found.
func loadQuote(db *gorm.DB, tenantID, id string) *models.Quote {
	var quote models.Quote
	if err := db.Preload("Items", func(d *gorm.DB) *gorm.DB {
		return d.Order("quote_items.sort_order ASC")
	}).
		Preload("Customer").
		Where("id = ? AND tenant_id = ?", id, tenantID).
		First(&quote).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return nil
	}
	return &quote
}
