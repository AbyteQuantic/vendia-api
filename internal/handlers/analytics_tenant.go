package handlers

import (
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func AnalyticsDashboard(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		now := time.Now()
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

		var totalSales float64
		var transactionCount int64
		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, startOfToday).
			Count(&transactionCount).
			Select("COALESCE(SUM(total), 0)").
			Scan(&totalSales)

		var totalCredit float64
		db.Model(&models.CreditAccount{}).
			Where("tenant_id = ? AND status IN ('open', 'partial')", tenantID).
			Select("COALESCE(SUM(total_amount - paid_amount), 0)").
			Scan(&totalCredit)

		var productCount int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true", tenantID).
			Count(&productCount)

		var lowStockCount int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND stock <= min_stock AND min_stock > 0", tenantID).
			Count(&lowStockCount)

		var pendingOrders int64
		db.Model(&models.OrderTicket{}).
			Where("tenant_id = ? AND status IN ('nuevo', 'preparando')", tenantID).
			Count(&pendingOrders)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"total_sales_today":   totalSales,
				"transaction_count":   transactionCount,
				"total_credit_pending": totalCredit,
				"product_count":       productCount,
				"low_stock_count":     lowStockCount,
				"pending_orders":      pendingOrders,
			},
		})
	}
}

// FinancialSummary returns aggregated financial data by payment method.
// GET /api/v1/analytics/financial-summary?period=today|week|month
func FinancialSummary(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		period := c.DefaultQuery("period", "today")

		now := time.Now()
		var since time.Time
		switch period {
		case "week":
			since = now.AddDate(0, 0, -7)
		case "month":
			since = now.AddDate(0, -1, 0)
		default: // today
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		}

		// Total sales by payment method
		type MethodTotal struct {
			PaymentMethod string  `json:"payment_method"`
			Total         float64 `json:"total"`
			Count         int64   `json:"count"`
		}
		var byMethod []MethodTotal
		db.Model(&models.Sale{}).
			Select("payment_method, COALESCE(SUM(total), 0) as total, COUNT(*) as count").
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, since).
			Group("payment_method").
			Scan(&byMethod)

		var totalSales float64
		var totalCount int64
		cashInDrawer := 0.0
		digitalMoney := 0.0
		for _, m := range byMethod {
			totalSales += m.Total
			totalCount += m.Count
			switch m.PaymentMethod {
			case "cash":
				cashInDrawer = m.Total
			case "transfer", "card", "nequi", "daviplata":
				digitalMoney += m.Total
			}
		}

		// Accounts receivable (open credits)
		var accountsReceivable float64
		db.Model(&models.CreditAccount{}).
			Where("tenant_id = ? AND status IN ('open', 'partial')", tenantID).
			Select("COALESCE(SUM(total_amount - paid_amount), 0)").
			Scan(&accountsReceivable)

		// Profit estimate (sales - cost)
		var totalCost float64
		db.Model(&models.SaleItem{}).
			Select("COALESCE(SUM(sale_items.quantity * p.purchase_price), 0)").
			Joins("JOIN sales ON sales.id = sale_items.sale_id").
			Joins("JOIN products p ON p.id = sale_items.product_id").
			Where("sales.tenant_id = ? AND sales.created_at >= ? AND sales.deleted_at IS NULL",
				tenantID, since).
			Scan(&totalCost)

		// Daily average (last 30 days)
		thirtyDaysAgo := now.AddDate(0, 0, -30)
		var last30Total float64
		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, thirtyDaysAgo).
			Select("COALESCE(SUM(total), 0)").
			Scan(&last30Total)
		dailyAvg := last30Total / 30

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"total_sales":          totalSales,
				"transaction_count":    totalCount,
				"total_profit":         totalSales - totalCost,
				"daily_average":        dailyAvg,
				"cash_in_drawer":       cashInDrawer,
				"digital_money":        digitalMoney,
				"accounts_receivable":  accountsReceivable,
				"by_method":            byMethod,
			},
		})
	}
}

// SalesHistoryByPeriod returns paginated sales filtered by period.
// GET /api/v1/analytics/sales-history?period=today&page=1&per_page=20
func SalesHistoryByPeriod(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		period := c.DefaultQuery("period", "today")
		p := parsePagination(c)

		now := time.Now()
		var since time.Time
		switch period {
		case "week":
			since = now.AddDate(0, 0, -7)
		case "month":
			since = now.AddDate(0, -1, 0)
		default:
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		}

		var total int64
		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, since).
			Count(&total)

		var sales []models.Sale
		db.Preload("Items").
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, since).
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&sales)

		c.JSON(http.StatusOK, newPaginatedResponse(sales, total, p))
	}
}

func TopProducts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		period := c.DefaultQuery("period", "7d")

		var since time.Time
		switch period {
		case "1d":
			since = time.Now().AddDate(0, 0, -1)
		case "7d":
			since = time.Now().AddDate(0, 0, -7)
		case "30d":
			since = time.Now().AddDate(0, 0, -30)
		default:
			since = time.Now().AddDate(0, 0, -7)
		}

		type TopProduct struct {
			ProductID string  `json:"product_id"`
			Name      string  `json:"name"`
			Quantity  int     `json:"quantity"`
			Revenue   float64 `json:"revenue"`
		}

		var top []TopProduct
		db.Model(&models.SaleItem{}).
			Select("sale_items.product_id, sale_items.name, SUM(sale_items.quantity) as quantity, SUM(sale_items.subtotal) as revenue").
			Joins("JOIN sales ON sales.id = sale_items.sale_id").
			Where("sales.tenant_id = ? AND sales.created_at >= ? AND sales.deleted_at IS NULL", tenantID, since).
			Group("sale_items.product_id, sale_items.name").
			Order("quantity DESC").
			Limit(20).
			Scan(&top)

		c.JSON(http.StatusOK, gin.H{"data": top})
	}
}

func PhotoCoverage(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var totalProducts int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true", tenantID).
			Count(&totalProducts)

		var withPhoto int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND (photo_url != '' OR image_url != '')", tenantID).
			Count(&withPhoto)

		percentage := float64(0)
		if totalProducts > 0 {
			percentage = float64(withPhoto) / float64(totalProducts) * 100
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"total_products": totalProducts,
				"with_photo":     withPhoto,
				"without_photo":  totalProducts - withPhoto,
				"percentage":     percentage,
			},
		})
	}
}

func SalesByEmployee(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		now := time.Now()
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

		type EmployeeSales struct {
			EmployeeUUID string  `json:"employee_uuid"`
			EmployeeName string  `json:"employee_name"`
			SaleCount    int64   `json:"sale_count"`
			TotalAmount  float64 `json:"total_amount"`
		}

		var result []EmployeeSales
		db.Model(&models.Sale{}).
			Select("employee_uuid, employee_name, COUNT(*) as sale_count, SUM(total) as total_amount").
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL AND employee_uuid != ''",
				tenantID, startOfToday).
			Group("employee_uuid, employee_name").
			Order("total_amount DESC").
			Scan(&result)

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}

func InventoryHealth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var totalValue float64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND stock > 0", tenantID).
			Select("COALESCE(SUM(purchase_price * stock), 0)").
			Scan(&totalValue)

		var totalRetailValue float64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND stock > 0", tenantID).
			Select("COALESCE(SUM(price * stock), 0)").
			Scan(&totalRetailValue)

		var lowStockCount int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND stock <= min_stock AND min_stock > 0", tenantID).
			Count(&lowStockCount)

		var outOfStockCount int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND stock = 0", tenantID).
			Count(&outOfStockCount)

		sevenDays := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
		var expiringCount int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND expiry_date IS NOT NULL AND expiry_date <= ?", tenantID, sevenDays).
			Count(&expiringCount)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"inventory_cost_value":   totalValue,
				"inventory_retail_value": totalRetailValue,
				"potential_profit":       totalRetailValue - totalValue,
				"low_stock_count":        lowStockCount,
				"out_of_stock_count":     outOfStockCount,
				"expiring_count":         expiringCount,
			},
		})
	}
}

func IngestionMethod(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		type MethodCount struct {
			Method string `json:"method"`
			Count  int64  `json:"count"`
		}

		var result []MethodCount
		db.Model(&models.Product{}).
			Select("ingestion_method as method, COUNT(*) as count").
			Where("tenant_id = ? AND is_available = true", tenantID).
			Group("ingestion_method").
			Scan(&result)

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}
