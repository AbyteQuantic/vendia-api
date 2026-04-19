package handlers

import (
	"fmt"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SaleItemRequest struct {
	ProductID    string `json:"product_id" binding:"required"`
	Quantity     int    `json:"quantity"   binding:"required,min=1"`
	HasContainer *bool  `json:"has_container"`
}

type CreateSaleRequest struct {
	ID            string               `json:"id"`
	Items         []SaleItemRequest    `json:"items"          binding:"required,min=1"`
	PaymentMethod models.PaymentMethod `json:"payment_method" binding:"required"`
	CustomerID    *string              `json:"customer_id"`
}

func CreateSale(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		branchID := middleware.GetBranchID(c)

		var req CreateSaleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a valid UUID v4"})
			return
		}

		if req.PaymentMethod == models.PaymentCredit && (req.CustomerID == nil || *req.CustomerID == "") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "customer_id requerido para ventas a crédito"})
			return
		}

		var sale models.Sale
		err := db.Transaction(func(tx *gorm.DB) error {
			var items []models.SaleItem
			var total float64

			for _, item := range req.Items {
				var product models.Product
				if err := tx.Where("id = ? AND tenant_id = ? AND is_available = true", item.ProductID, tenantID).
					First(&product).Error; err != nil {
					return &gin.Error{Err: errProductNotFound(item.ProductID), Type: gin.ErrorTypePublic}
				}

				subtotal := product.Price * float64(item.Quantity)
				total += subtotal

				items = append(items, models.SaleItem{
					ProductID: product.ID,
					Name:      product.Name,
					Price:     product.Price,
					Quantity:  item.Quantity,
					Subtotal:  subtotal,
				})

				if product.RequiresContainer && (item.HasContainer == nil || !*item.HasContainer) {
					containerSubtotal := float64(product.ContainerPrice) * float64(item.Quantity)
					total += containerSubtotal
					items = append(items, models.SaleItem{
						ProductID:         product.ID,
						Name:              product.Name + " — Envase",
						Price:             float64(product.ContainerPrice),
						Quantity:          item.Quantity,
						Subtotal:          containerSubtotal,
						IsContainerCharge: true,
					})
				}

				if product.Stock > 0 {
					tx.Model(&product).UpdateColumn("stock", gorm.Expr("stock - ?", item.Quantity))
				}
			}

			sale = models.Sale{
				TenantID:      tenantID,
				CreatedBy:     userID,
				BranchID:      branchID,
				Total:         total,
				PaymentMethod: req.PaymentMethod,
				CustomerID:    req.CustomerID,
				IsCredit:      req.PaymentMethod == models.PaymentCredit,
				Items:         items,
			}
			if req.ID != "" {
				sale.ID = req.ID
			}

			if err := tx.Create(&sale).Error; err != nil {
				return err
			}

			if sale.IsCredit && req.CustomerID != nil {
				credit := models.CreditAccount{
					TenantID:    tenantID,
					CreatedBy:   userID,
					BranchID:    branchID,
					CustomerID:  *req.CustomerID,
					SaleID:      &sale.ID,
					TotalAmount: int64(total),
					Status:      "open",
				}
				return tx.Create(&credit).Error
			}

			return nil
		})

		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": sale})
	}
}

func TodayStats(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		now := time.Now()
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		startOfYesterday := startOfToday.AddDate(0, 0, -1)

		var totalSales float64
		var transactionCount int64

		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, startOfToday).
			Count(&transactionCount).
			Select("COALESCE(SUM(total), 0)").
			Scan(&totalSales)

		var yesterdaySales float64
		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL",
				tenantID, startOfYesterday, startOfToday).
			Select("COALESCE(SUM(total), 0)").
			Scan(&yesterdaySales)

		trend := "primer día"
		if yesterdaySales > 0 {
			pct := ((totalSales - yesterdaySales) / yesterdaySales) * 100
			if pct >= 0 {
				trend = fmt.Sprintf("+%.0f%%", pct)
			} else {
				trend = fmt.Sprintf("%.0f%%", pct)
			}
		} else if totalSales > 0 {
			trend = "+100%"
		}

		type TopProduct struct {
			Name     string `json:"name"`
			Quantity int    `json:"quantity"`
		}
		var top TopProduct
		db.Model(&models.SaleItem{}).
			Select("sale_items.name, SUM(sale_items.quantity) as quantity").
			Joins("JOIN sales ON sales.id = sale_items.sale_id").
			Where("sales.tenant_id = ? AND sales.created_at >= ? AND sales.deleted_at IS NULL", tenantID, startOfToday).
			Group("sale_items.name").
			Order("quantity DESC").
			Limit(1).
			Scan(&top)

		topName := top.Name
		if topName == "" {
			topName = "—"
		}

		c.JSON(http.StatusOK, gin.H{
			"total_sales_today": totalSales,
			"transaction_count": transactionCount,
			"top_product":       topName,
			"trend":             trend,
		})
	}
}

func ListSales(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		var total int64
		query := db.Model(&models.Sale{}).Where("tenant_id = ?", tenantID)
		query.Count(&total)

		var sales []models.Sale
		if err := db.Preload("Items").
			Where("tenant_id = ?", tenantID).
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&sales).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener ventas"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(sales, total, p))
	}
}

type productNotFoundError struct{ id string }

func (e *productNotFoundError) Error() string {
	return "producto no encontrado o no disponible"
}

func errProductNotFound(id string) error { return &productNotFoundError{id: id} }
