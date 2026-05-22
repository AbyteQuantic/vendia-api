// Spec: specs/031-cotizaciones/spec.md
package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SendQuote moves a quote from `borrador` to `enviada` and stamps
// sent_at. It does NOT send anything itself — the Flutter client builds
// the WhatsApp / link / PDF channel from the returned public_token
// (Spec plan §4). FSM-checked.
// POST /api/v1/quotes/:id/send
func SendQuote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var quote models.Quote
		if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
			First(&quote).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		if !services.CanTransition(quote.Status, models.QuoteStatusSent) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "solo se puede enviar una cotización en borrador",
			})
			return
		}

		now := time.Now().UTC()
		if err := db.Model(&models.Quote{}).Where("id = ?", quote.ID).
			Updates(map[string]any{
				"status":  models.QuoteStatusSent,
				"sent_at": now,
			}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo enviar la cotización"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": loadQuote(db, tenantID, quote.ID)})
	}
}

// MarkQuoteStatus lets the owner manually move a quote to `aprobada` or
// `rechazada` — used when the customer accepts/rejects verbally instead
// of through the public link (Spec plan §4). FSM-checked.
// POST /api/v1/quotes/:id/mark-status
func MarkQuoteStatus(db *gorm.DB) gin.HandlerFunc {
	type request struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// Only approve/reject are valid manual targets — send has its own
		// endpoint, convert has its own, and the rest are system states.
		if req.Status != models.QuoteStatusApproved && req.Status != models.QuoteStatusRejected {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "estado inválido: solo 'aprobada' o 'rechazada'",
			})
			return
		}

		var quote models.Quote
		if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
			First(&quote).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		if !services.CanTransition(quote.Status, req.Status) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "transición de estado no permitida desde " + quote.Status,
			})
			return
		}

		updates := map[string]any{
			"status":     req.Status,
			"decided_at": time.Now().UTC(),
		}
		if note := strings.TrimSpace(req.Note); note != "" {
			updates["note"] = note
		}
		if err := db.Model(&models.Quote{}).Where("id = ?", quote.ID).
			Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el estado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": loadQuote(db, tenantID, quote.ID)})
	}
}

// ConvertQuote turns an `aprobada` quote into a Sale (Spec F031 AC-09).
// It creates the Sale with the quote's items, applies the inventory side
// effects via the shared SaleInventoryService (so stock is discounted
// exactly like a POS sale), links sale.quote_id ↔ quote.sale_id, and
// moves the quote to `convertida`. FSM-checked.
// POST /api/v1/quotes/:id/convert
func ConvertQuote(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)

		quote := loadQuote(db, tenantID, c.Param("id"))
		if quote == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cotización no encontrada"})
			return
		}

		if !services.CanTransition(quote.Status, models.QuoteStatusConverted) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "solo se puede convertir una cotización aprobada",
			})
			return
		}

		// Snapshot the customer identity onto the sale — reprinting the
		// receipt must not depend on the Customer row still matching
		// (mirrors CreateSale's F030 contract).
		customerName, customerPhone := "", ""
		if quote.Customer != nil {
			customerName = quote.Customer.Name
			customerPhone = quote.Customer.Phone
		}

		saleInventory := services.NewSaleInventoryService(db)

		var sale models.Sale
		err := db.Transaction(func(tx *gorm.DB) error {
			items := make([]models.SaleItem, 0, len(quote.Items))
			inventoryLines := make([]services.SaleInventoryLine, 0)

			for _, qi := range quote.Items {
				qty := int(qi.Quantity)
				if qty < 1 {
					qty = 1
				}
				si := models.SaleItem{
					Name:     qi.Name,
					Price:    qi.UnitPrice,
					Quantity: qty,
					Subtotal: qi.Subtotal,
				}
				if qi.ProductID != nil {
					pid := *qi.ProductID
					si.ProductID = &pid
					inventoryLines = append(inventoryLines, services.SaleInventoryLine{
						ProductID: pid,
						Quantity:  qty,
					})
				} else {
					// Free quote line → ad-hoc service line on the sale.
					si.IsService = true
					si.CustomDescription = qi.Name
					si.CustomUnitPrice = qi.UnitPrice
				}
				items = append(items, si)
			}

			cid := quote.CustomerID
			qid := quote.ID
			sale = models.Sale{
				TenantID:              tenantID,
				CreatedBy:             middleware.UUIDPtr(userID),
				EmployeeUUID:          middleware.UUIDPtr(userID),
				Total:                 quote.Total,
				TaxAmount:             quote.TaxAmount,
				PaymentMethod:         models.PaymentCash,
				CustomerID:            &cid,
				CustomerNameSnapshot:  customerName,
				CustomerPhoneSnapshot: customerPhone,
				PaymentStatus:         "COMPLETED",
				PriceTier:             models.PriceTierRetail,
				Source:                models.SaleSourcePOS,
				QuoteID:               &qid,
				Items:                 items,
			}
			if err := tx.Create(&sale).Error; err != nil {
				return err
			}

			if err := saleInventory.ApplyPostSale(tx, services.PostSaleParams{
				TenantID: tenantID,
				SaleUUID: sale.ID,
				UserID:   middleware.UUIDPtr(userID),
				Lines:    inventoryLines,
			}); err != nil {
				return err
			}

			// Link the quote → sale and move it to convertida.
			return tx.Model(&models.Quote{}).Where("id = ?", quote.ID).
				Updates(map[string]any{
					"status":     models.QuoteStatusConverted,
					"sale_id":    sale.ID,
					"decided_at": time.Now().UTC(),
				}).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo convertir la cotización en venta",
			})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"sale_id": sale.ID,
				"quote":   loadQuote(db, tenantID, quote.ID),
			},
		})
	}
}

// quoteExpiredErr is returned by lazyExpireQuote when a read finds a
// quote past its validity — kept distinct so the public GET can still
// render it (just without the approve/reject buttons).
var quoteExpiredErr = errors.New("quote expired")

// lazyExpireQuote moves a `enviada` quote to `vencida` in place when its
// valid_until has passed (Spec plan D7 — lazy check as a safety net for
// the cron). Mutates the passed quote so the caller sees the new status.
// A no-op for any other state.
func lazyExpireQuote(db *gorm.DB, quote *models.Quote) {
	if quote.Status != models.QuoteStatusSent {
		return
	}
	if !time.Now().UTC().After(quote.ValidUntil) {
		return
	}
	if !services.CanTransition(quote.Status, models.QuoteStatusExpired) {
		return
	}
	if err := db.Model(&models.Quote{}).Where("id = ?", quote.ID).
		Update("status", models.QuoteStatusExpired).Error; err == nil {
		quote.Status = models.QuoteStatusExpired
	}
}
