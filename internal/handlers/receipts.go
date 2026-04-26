package handlers

import (
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SalesHistory — GET /api/v1/sales/history
//
// Unified ledger across POS, table-close and web-order channels.
// Items are preloaded so the cashier can reconstruct the receipt
// without a second round-trip per row.
//
// Filters (all optional, AND-combined):
//
//	date            single day (YYYY-MM-DD) — backward-compat with
//	                old clients; takes precedence over the range
//	                params if both are sent.
//	start_date      lower bound, inclusive (YYYY-MM-DD).
//	end_date        upper bound, inclusive (YYYY-MM-DD). Treated as
//	                end-of-day so "2026-04-26" includes 23:59.
//	payment_method  cash / transfer / card / credit
//	source          POS / WEB / TABLE
//	query           substring match on receipt_number for receipt
//	                lookups by paper sticker.
func SalesHistory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		dateStr := c.Query("date")
		startStr := c.Query("start_date")
		endStr := c.Query("end_date")
		paymentMethod := c.Query("payment_method")
		source := c.Query("source")
		query := c.Query("query")

		dbQuery := db.Model(&models.Sale{}).Where("tenant_id = ?", tenantID)

		switch {
		case dateStr != "":
			date, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "formato de fecha inválido (use YYYY-MM-DD)",
				})
				return
			}
			nextDay := date.AddDate(0, 0, 1)
			dbQuery = dbQuery.Where("created_at >= ? AND created_at < ?", date, nextDay)
		case startStr != "" || endStr != "":
			if startStr != "" {
				start, err := time.Parse("2006-01-02", startStr)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": "start_date inválido (use YYYY-MM-DD)",
					})
					return
				}
				dbQuery = dbQuery.Where("created_at >= ?", start)
			}
			if endStr != "" {
				end, err := time.Parse("2006-01-02", endStr)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": "end_date inválido (use YYYY-MM-DD)",
					})
					return
				}
				// inclusive upper bound: + 1 day → strict less-than.
				inclusive := end.AddDate(0, 0, 1)
				dbQuery = dbQuery.Where("created_at < ?", inclusive)
			}
		}

		if paymentMethod != "" {
			dbQuery = dbQuery.Where("payment_method = ?", paymentMethod)
		}
		if source != "" {
			// Source vocab is the lowercased Sale.Source enum but
			// clients tend to send "WEB" / "POS" — accept both
			// shapes by uppercasing on the wire.
			dbQuery = dbQuery.Where("source = ?", strings.ToUpper(source))
		}

		if query != "" {
			dbQuery = dbQuery.Where("CAST(receipt_number AS TEXT) LIKE ?", "%"+query+"%")
		}

		var total int64
		dbQuery.Count(&total)

		var sales []models.Sale
		if err := dbQuery.Preload("Items").
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&sales).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener historial"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(sales, total, p))
	}
}

func SaleReceipt(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		saleUUID := c.Param("uuid")

		var sale models.Sale
		if err := db.Preload("Items").
			Where("id = ? AND tenant_id = ?", saleUUID, tenantID).
			First(&sale).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "venta no encontrada"})
			return
		}

		var tenant models.Tenant
		db.Where("id = ?", tenantID).First(&tenant)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"sale":          sale,
				"business_name": tenant.BusinessName,
				"nit":           tenant.NIT,
				"address":       tenant.Address,
				"phone":         tenant.Phone,
			},
		})
	}
}

func ReprintReceipt(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		saleUUID := c.Param("uuid")

		var sale models.Sale
		if err := db.Preload("Items").
			Where("id = ? AND tenant_id = ?", saleUUID, tenantID).
			First(&sale).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "venta no encontrada"})
			return
		}

		var tenant models.Tenant
		db.Where("id = ?", tenantID).First(&tenant)

		type ReceiptItem struct {
			Name     string  `json:"name"`
			Quantity int     `json:"quantity"`
			Price    float64 `json:"price"`
			Subtotal float64 `json:"subtotal"`
		}

		var items []ReceiptItem
		for _, item := range sale.Items {
			items = append(items, ReceiptItem{
				Name:     item.Name,
				Quantity: item.Quantity,
				Price:    item.Price,
				Subtotal: item.Subtotal,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"receipt_number":  sale.ReceiptNumber,
				"business_name":  tenant.BusinessName,
				"nit":            tenant.NIT,
				"address":        tenant.Address,
				"phone":          tenant.Phone,
				"items":          items,
				"total":          sale.Total,
				"payment_method": sale.PaymentMethod,
				"employee_name":  sale.EmployeeName,
				"date":           sale.CreatedAt.Format("2006-01-02 15:04:05"),
			},
		})
	}
}
