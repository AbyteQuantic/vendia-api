package handlers

import (
	"encoding/json"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type TabItem struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Quantity  int     `json:"quantity"`
}

func ListOpenTabs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		status := c.DefaultQuery("status", "open")

		var tabs []models.OpenTab
		if err := db.Preload("Table").
			Where("tenant_id = ? AND status = ?", tenantID, status).
			Order("opened_at DESC").
			Find(&tabs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener cuentas"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": tabs, "count": len(tabs)})
	}
}

func OpenTab(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		TableID string    `json:"table_id" binding:"required"`
		Items   []TabItem `json:"items"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		itemsJSON := "[]"
		if len(req.Items) > 0 {
			b, _ := json.Marshal(req.Items)
			itemsJSON = string(b)
		}

		tab := models.OpenTab{
			TenantID: tenantID,
			TableID:  req.TableID,
			Status:   "open",
			Items:    itemsJSON,
			OpenedAt: time.Now(),
		}

		if err := db.Create(&tab).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al abrir cuenta"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": tab})
	}
}

func AddItemsToTab(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Items []TabItem `json:"items" binding:"required,min=1"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		tabID := c.Param("id")

		var tab models.OpenTab
		if err := db.Where("id = ? AND tenant_id = ? AND status = 'open'", tabID, tenantID).
			First(&tab).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cuenta no encontrada o ya cerrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var existing []TabItem
		if tab.Items != "" && tab.Items != "[]" {
			_ = json.Unmarshal([]byte(tab.Items), &existing)
		}

		existing = append(existing, req.Items...)
		b, _ := json.Marshal(existing)

		if err := db.Model(&tab).Update("items", string(b)).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al agregar ítems"})
			return
		}

		tab.Items = string(b)
		c.JSON(http.StatusOK, gin.H{"data": tab})
	}
}

func CloseTab(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PaymentMethod models.PaymentMethod `json:"payment_method" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		tabID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var tab models.OpenTab
		if err := db.Where("id = ? AND tenant_id = ? AND status = 'open'", tabID, tenantID).
			First(&tab).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cuenta no encontrada o ya cerrada"})
			return
		}

		var tabItems []TabItem
		if err := json.Unmarshal([]byte(tab.Items), &tabItems); err != nil || len(tabItems) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la cuenta no tiene ítems"})
			return
		}

		var sale models.Sale
		err := db.Transaction(func(tx *gorm.DB) error {
			var saleItems []models.SaleItem
			var total float64

			for _, item := range tabItems {
				subtotal := item.Price * float64(item.Quantity)
				total += subtotal
				saleItems = append(saleItems, models.SaleItem{
					ProductID: item.ProductID,
					Name:      item.Name,
					Price:     item.Price,
					Quantity:  item.Quantity,
					Subtotal:  subtotal,
				})

				tx.Model(&models.Product{}).
					Where("id = ? AND tenant_id = ? AND stock > 0", item.ProductID, tenantID).
					UpdateColumn("stock", gorm.Expr("stock - ?", item.Quantity))
			}

			sale = models.Sale{
				TenantID:      tenantID,
				Total:         total,
				PaymentMethod: req.PaymentMethod,
				Items:         saleItems,
			}
			if err := tx.Create(&sale).Error; err != nil {
				return err
			}

			now := time.Now()
			return tx.Model(&tab).Updates(map[string]any{
				"status":    "closed",
				"closed_at": now,
				"sale_id":   sale.ID,
			}).Error
		})

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al cerrar cuenta"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": sale, "tab_id": tab.ID})
	}
}
