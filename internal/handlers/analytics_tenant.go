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
