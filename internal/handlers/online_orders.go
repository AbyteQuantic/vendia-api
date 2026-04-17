package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PublicCreateOnlineOrder creates an order from the public catalog.
// POST /api/v1/store/:slug/online-order (no auth)
func PublicCreateOnlineOrder(db *gorm.DB) gin.HandlerFunc {
	type ItemReq struct {
		ProductID string  `json:"product_id"`
		Name      string  `json:"name"`
		Quantity  int     `json:"quantity"`
		Price     float64 `json:"price"`
	}
	type Request struct {
		CustomerName  string    `json:"customer_name" binding:"required"`
		CustomerPhone string    `json:"customer_phone" binding:"required"`
		DeliveryType  string    `json:"delivery_type"`
		Items         []ItemReq `json:"items" binding:"required,min=1"`
		Notes         string    `json:"notes"`
	}

	return func(c *gin.Context) {
		slug := c.Param("slug")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var total float64
		for _, item := range req.Items {
			total += item.Price * float64(item.Quantity)
		}

		delivery := req.DeliveryType
		if delivery == "" {
			delivery = "pickup"
		}

		itemsJSON, _ := json.Marshal(req.Items)

		order := models.OnlineOrder{
			TenantID:      tenant.ID,
			CustomerName:  req.CustomerName,
			CustomerPhone: req.CustomerPhone,
			DeliveryType:  delivery,
			Status:        "pending",
			TotalAmount:   total,
			Items:         string(itemsJSON),
			Notes:         req.Notes,
		}

		if err := db.Create(&order).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear pedido"})
			return
		}

		// Create notification for the tenant
		CreateNotification(db, tenant.ID,
			"Nuevo pedido en línea",
			fmt.Sprintf("%s pidió por $%.0f (%s)", req.CustomerName, total, delivery),
			"online_order",
		)

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"order_id": order.ID,
				"total":    total,
				"status":   order.Status,
			},
		})
	}
}

// ListOnlineOrders returns orders for the tenant.
func ListOnlineOrders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		status := c.DefaultQuery("status", "")

		query := db.Where("tenant_id = ?", tenantID)
		if status != "" {
			query = query.Where("status = ?", status)
		}

		var orders []models.OnlineOrder
		query.Order("created_at DESC").Limit(50).Find(&orders)

		c.JSON(http.StatusOK, gin.H{"data": orders})
	}
}

// UpdateOnlineOrderStatus changes order status.
func UpdateOnlineOrderStatus(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Status string `json:"status" binding:"required"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		orderID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		result := db.Model(&models.OnlineOrder{}).
			Where("id = ? AND tenant_id = ?", orderID, tenantID).
			Update("status", req.Status)

		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "estado actualizado"})
	}
}
