package handlers

import (
	"fmt"
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetStoreConfig(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"store_slug":       tenant.StoreSlug,
				"is_delivery_open": tenant.IsDeliveryOpen,
				"delivery_cost":    tenant.DeliveryCost,
				"min_order_amount": tenant.MinOrderAmount,
				"logo_url":         tenant.LogoURL,
				"business_name":    tenant.BusinessName,
			},
		})
	}
}

func UpdateStoreConfig(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		StoreSlug      *string  `json:"store_slug"`
		IsDeliveryOpen *bool    `json:"is_delivery_open"`
		DeliveryCost   *float64 `json:"delivery_cost"`
		MinOrderAmount *float64 `json:"min_order_amount"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.StoreSlug != nil {
			var existing models.Tenant
			if err := db.Where("store_slug = ? AND id != ?", *req.StoreSlug, tenantID).
				First(&existing).Error; err == nil {
				c.JSON(http.StatusConflict, gin.H{"error": "ese slug ya está en uso"})
				return
			}
			updates["store_slug"] = *req.StoreSlug
		}
		if req.IsDeliveryOpen != nil {
			updates["is_delivery_open"] = *req.IsDeliveryOpen
		}
		if req.DeliveryCost != nil {
			updates["delivery_cost"] = *req.DeliveryCost
		}
		if req.MinOrderAmount != nil {
			updates["min_order_amount"] = *req.MinOrderAmount
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar configuración"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "configuración actualizada"})
	}
}

func PublicCatalog(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := c.Param("slug")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		if !tenant.IsDeliveryOpen {
			c.JSON(http.StatusOK, gin.H{
				"data": gin.H{
					"business_name": tenant.BusinessName,
					"is_open":       false,
					"products":      []any{},
				},
			})
			return
		}

		var products []models.Product
		db.Where("tenant_id = ? AND is_available = true AND price > 0", tenant.ID).
			Order("name ASC").
			Find(&products)

		type CatalogProduct struct {
			UUID     string  `json:"uuid"`
			Name     string  `json:"name"`
			Price    float64 `json:"price"`
			PhotoURL string  `json:"photo_url"`
			Emoji    string  `json:"emoji"`
			Category string  `json:"category"`
		}

		var catalog []CatalogProduct
		for _, p := range products {
			photo := p.PhotoURL
			if photo == "" {
				photo = p.ImageURL
			}
			catalog = append(catalog, CatalogProduct{
				UUID:     p.ID,
				Name:     p.Name,
				Price:    p.Price,
				PhotoURL: photo,
				Emoji:    p.Emoji,
				Category: p.Category,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name":   tenant.BusinessName,
				"logo_url":        tenant.LogoURL,
				"is_open":         true,
				"delivery_cost":   tenant.DeliveryCost,
				"min_order_amount": tenant.MinOrderAmount,
				"products":        catalog,
			},
		})
	}
}

func PublicProductDetail(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := c.Param("slug")
		productUUID := c.Param("uuid")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ? AND is_available = true", productUUID, tenant.ID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		photo := product.PhotoURL
		if photo == "" {
			photo = product.ImageURL
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"uuid":     product.ID,
				"name":     product.Name,
				"price":    product.Price,
				"photo_url": photo,
				"emoji":    product.Emoji,
				"category": product.Category,
			},
		})
	}
}

func CreateWebOrder(db *gorm.DB) gin.HandlerFunc {
	type ItemReq struct {
		ProductUUID string  `json:"product_uuid" binding:"required"`
		Quantity    int     `json:"quantity"      binding:"required,min=1"`
	}

	type Request struct {
		CustomerName    string    `json:"customer_name"     binding:"required"`
		CustomerPhone   string    `json:"customer_phone"    binding:"required"`
		DeliveryAddress string    `json:"delivery_address"  binding:"required"`
		Items           []ItemReq `json:"items"             binding:"required,min=1"`
	}

	return func(c *gin.Context) {
		slug := c.Param("slug")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		if !tenant.IsDeliveryOpen {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la tienda no está aceptando pedidos"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var total float64
		var orderItems []models.OrderItem
		for _, item := range req.Items {
			var product models.Product
			if err := db.Where("id = ? AND tenant_id = ? AND is_available = true", item.ProductUUID, tenant.ID).
				First(&product).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "producto no disponible: " + item.ProductUUID})
				return
			}

			subtotal := product.Price * float64(item.Quantity)
			total += subtotal
			orderItems = append(orderItems, models.OrderItem{
				ProductUUID: product.ID,
				ProductName: product.Name,
				Quantity:    item.Quantity,
				UnitPrice:   product.Price,
				Emoji:       product.Emoji,
			})
		}

		if tenant.MinOrderAmount > 0 && total < tenant.MinOrderAmount {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "el pedido mínimo es de $" + formatAmount(tenant.MinOrderAmount),
			})
			return
		}

		total += tenant.DeliveryCost

		order := models.OrderTicket{
			TenantID:        tenant.ID,
			Label:           "🌐 Domicilio Web",
			CustomerName:    req.CustomerName,
			CustomerPhone:   req.CustomerPhone,
			DeliveryAddress: req.DeliveryAddress,
			Status:          models.OrderStatusNuevo,
			Type:            models.OrderTypeDomicilioWeb,
			Total:           total,
			Items:           orderItems,
		}

		if err := db.Create(&order).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear pedido"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"order_uuid":    order.ID,
				"status":        order.Status,
				"total":         order.Total,
				"delivery_cost": tenant.DeliveryCost,
			},
		})
	}
}

func GetWebOrderStatus(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := c.Param("slug")
		orderUUID := c.Param("uuid")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var order models.OrderTicket
		if err := db.Preload("Items").
			Where("id = ? AND tenant_id = ?", orderUUID, tenant.ID).
			First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": order})
	}
}

func formatAmount(amount float64) string {
	return fmt.Sprintf("%.0f", amount)
}
