package handlers

import (
	"net/http"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetAccountHTTP(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		orderUUID := c.Param("order_uuid")

		var order models.OrderTicket
		if err := db.Preload("Items").
			Where("id = ?", orderUUID).
			First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cuenta no encontrada"})
			return
		}

		type AccountItem struct {
			ProductName string  `json:"product_name"`
			Quantity    int     `json:"quantity"`
			UnitPrice   float64 `json:"unit_price"`
			Subtotal    float64 `json:"subtotal"`
			Emoji       string  `json:"emoji"`
		}

		var items []AccountItem
		for _, item := range order.Items {
			items = append(items, AccountItem{
				ProductName: item.ProductName,
				Quantity:    item.Quantity,
				UnitPrice:   item.UnitPrice,
				Subtotal:    item.UnitPrice * float64(item.Quantity),
				Emoji:       item.Emoji,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"order_uuid":    order.ID,
				"label":         order.Label,
				"customer_name": order.CustomerName,
				"status":        order.Status,
				"total":         order.Total,
				"items":         items,
				"created_at":    order.CreatedAt,
			},
		})
	}
}

func VerifyAccountPhone(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Phone string `json:"phone" binding:"required"`
	}

	return func(c *gin.Context) {
		orderUUID := c.Param("order_uuid")

		var order models.OrderTicket
		if err := db.Where("id = ?", orderUUID).First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cuenta no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if order.CustomerPhone != "" && order.CustomerPhone != req.Phone {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "número de celular no coincide"})
			return
		}

		if order.CustomerPhone == "" {
			db.Model(&order).Update("customer_phone", req.Phone)
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"verified":   true,
				"order_uuid": order.ID,
				"status":     order.Status,
			},
		})
	}
}
