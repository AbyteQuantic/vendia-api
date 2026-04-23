package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
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
				"store_slug":          tenant.StoreSlug,
				"is_delivery_open":    tenant.IsDeliveryOpen,
				"delivery_cost":       tenant.DeliveryCost,
				"min_order_amount":    tenant.MinOrderAmount,
				"logo_url":            tenant.LogoURL,
				"business_name":       tenant.BusinessName,
				"enable_fiados":       tenant.EnableFiados,
				"default_margin":      tenant.DefaultMargin,
				"receipt_header":      tenant.ReceiptHeader,
				"receipt_footer":      tenant.ReceiptFooter,
				"printer_mac_address": tenant.PrinterMacAddress,
			},
		})
	}
}

func UpdateStoreConfig(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		StoreSlug         *string  `json:"store_slug"`
		IsDeliveryOpen    *bool    `json:"is_delivery_open"`
		DeliveryCost      *float64 `json:"delivery_cost"`
		MinOrderAmount    *float64 `json:"min_order_amount"`
		EnableFiados      *bool    `json:"enable_fiados"`
		DefaultMargin     *float64 `json:"default_margin"`
		ReceiptHeader     *string  `json:"receipt_header"`
		ReceiptFooter     *string  `json:"receipt_footer"`
		PrinterMacAddress *string  `json:"printer_mac_address"`
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
		if req.EnableFiados != nil {
			updates["enable_fiados"] = *req.EnableFiados
		}
		if req.DefaultMargin != nil {
			updates["default_margin"] = *req.DefaultMargin
		}
		if req.ReceiptHeader != nil {
			updates["receipt_header"] = *req.ReceiptHeader
		}
		if req.ReceiptFooter != nil {
			updates["receipt_footer"] = *req.ReceiptFooter
		}
		if req.PrinterMacAddress != nil {
			updates["printer_mac_address"] = *req.PrinterMacAddress
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar configuración"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "configuración actualizada"})
	}
}

// UpdatePaymentConfig — express setup of the tenant's primary digital
// payment method (Nequi/Daviplata/Bancolombia/Efectivo). Writes the
// three tenant fields the public fiado portal reads.
//
// PATCH /api/v1/store/payment-config
// body: {payment_method_name, payment_account_number, payment_account_holder}
func UpdateStoreStatus(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		IsOpen bool `json:"is_open"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).
			Update("is_delivery_open", req.IsOpen).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar estado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "estado actualizado", "is_open": req.IsOpen})
	}
}

func UpdatePaymentConfig(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PaymentMethodName    *string `json:"payment_method_name"`
		PaymentAccountNumber *string `json:"payment_account_number"`
		PaymentAccountHolder *string `json:"payment_account_holder"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.PaymentMethodName != nil {
			updates["payment_method_name"] = strings.TrimSpace(*req.PaymentMethodName)
		}
		if req.PaymentAccountNumber != nil {
			updates["payment_account_number"] =
				strings.TrimSpace(*req.PaymentAccountNumber)
		}
		if req.PaymentAccountHolder != nil {
			updates["payment_account_holder"] =
				strings.TrimSpace(*req.PaymentAccountHolder)
		}
		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no hay campos para actualizar"})
			return
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).
			Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al guardar configuración de pago",
			})
			return
		}

		// Return the fresh values so the client can reflect them immediately.
		var tenant models.Tenant
		db.Select("id, payment_method_name, payment_account_number, payment_account_holder").
			Where("id = ?", tenantID).First(&tenant)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"payment_method_name":    tenant.PaymentMethodName,
				"payment_account_number": tenant.PaymentAccountNumber,
				"payment_account_holder": tenant.PaymentAccountHolder,
			},
		})
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

		// Fetch every product of the tenant. We intentionally do NOT
		// filter by `is_available` or `price > 0` here:
		//   * `is_available` is POS-side metadata ("show in the quick
		//     grid") and gets flipped off by mistake more often than
		//     not — historically this wiped the entire public
		//     catalog for freshly-seeded tenants with test data.
		//   * `price = 0` is a legitimate state for products pending
		//     price entry and shouldn't hide them from the online
		//     catalog either (better to render them with "Precio por
		//     definir" than to ghost the store).
		// Stock/availability is still surfaced per-product so the
		// web can show "Agotado" or disable add-to-cart client-side.
		var products []models.Product
		db.Where("tenant_id = ?", tenant.ID).
			Order("name ASC").
			Find(&products)

		type CatalogProduct struct {
			UUID     string  `json:"uuid"`
			Name     string  `json:"name"`
			Price    float64 `json:"price"`
			PhotoURL string  `json:"photo_url"`
			Emoji    string  `json:"emoji"`
			Category string  `json:"category"`
			// Stock is exposed so the public catalog can display
			// availability ("X disponibles" / "Agotado") and disable
			// add-to-cart on zero stock — same source of truth as the
			// POS app, no separate "current_stock" column.
			Stock int `json:"stock"`
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
				Stock:    p.Stock,
			})
		}

		// Active promos (combo mode only — legacy single-product promos
		// don't have a banner and aren't a great carousel experience).
		// Filter to start_date <= now < end_date (or end_date NULL) and
		// include items so the web cart can pre-fill the combo lines.
		now := time.Now()
		var promos []models.Promotion
		db.Preload("Items").
			Where(`tenant_id = ? AND is_active = true
			       AND (start_date IS NULL OR start_date <= ?)
			       AND (end_date IS NULL OR end_date >= ?)
			       AND name <> ''`,
				tenant.ID, now, now).
			Order("start_date DESC NULLS LAST").
			Find(&promos)

		type PromoItemOut struct {
			ProductID  string  `json:"product_id"`
			Name       string  `json:"name"`
			Quantity   int     `json:"quantity"`
			PromoPrice float64 `json:"promo_price"`
			PhotoURL   string  `json:"photo_url,omitempty"`
		}
		type PromoOut struct {
			ID             string         `json:"id"`
			Name           string         `json:"name"`
			Description    string         `json:"description,omitempty"`
			BannerImageURL string         `json:"banner_image_url,omitempty"`
			Items          []PromoItemOut `json:"items"`
			TotalPrice     float64        `json:"total_price"`
			TotalRegular   float64        `json:"total_regular"`
		}

		// Build a product lookup so we can decorate items with names + photos.
		productByID := make(map[string]models.Product, len(products))
		for _, p := range products {
			productByID[p.ID] = p
		}

		promosOut := make([]PromoOut, 0, len(promos))
		for _, pr := range promos {
			items := make([]PromoItemOut, 0, len(pr.Items))
			var total, regular float64
			for _, it := range pr.Items {
				p := productByID[it.ProductID]
				photo := p.PhotoURL
				if photo == "" {
					photo = p.ImageURL
				}
				items = append(items, PromoItemOut{
					ProductID:  it.ProductID,
					Name:       p.Name,
					Quantity:   it.Quantity,
					PromoPrice: it.PromoPrice,
					PhotoURL:   photo,
				})
				total += it.PromoPrice * float64(it.Quantity)
				regular += p.Price * float64(it.Quantity)
			}
			promosOut = append(promosOut, PromoOut{
				ID:             pr.ID,
				Name:           pr.Name,
				Description:    pr.Description,
				BannerImageURL: pr.BannerImageURL,
				Items:          items,
				TotalPrice:     total,
				TotalRegular:   regular,
			})
		}

		// Fetch theme config
		var themeConfig models.TenantCatalogConfig
		db.Preload("Template").Where("tenant_id = ?", tenant.ID).First(&themeConfig)

		themeOut := gin.H{
			"primary_color": "#6366f1", // Default indigo
			"banner_url":    "",
		}
		if themeConfig.Template != nil {
			themeOut["primary_color"] = themeConfig.Template.PrimaryColorHex
			themeOut["banner_url"] = themeConfig.Template.DefaultBannerURL
		}
		if themeConfig.CustomLogoURL != "" {
			// Override logo if provided in theme config
			tenant.LogoURL = themeConfig.CustomLogoURL
		}

		businessType := ""
		if len(tenant.BusinessTypes) > 0 {
			businessType = tenant.BusinessTypes[0]
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name":    tenant.BusinessName,
				"business_type":    businessType,
				"phone":            tenant.Phone,
				"logo_url":         tenant.LogoURL,
				"is_open":          tenant.IsDeliveryOpen,
				"delivery_cost":    tenant.DeliveryCost,
				"min_order_amount": tenant.MinOrderAmount,
				"products":         catalog,
				"promotions":       promosOut,
				"theme":            themeOut,
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
				"uuid":      product.ID,
				"name":      product.Name,
				"price":     product.Price,
				"photo_url": photo,
				"emoji":     product.Emoji,
				"category":  product.Category,
			},
		})
	}
}

func CreateWebOrder(db *gorm.DB) gin.HandlerFunc {
	type ItemReq struct {
		ProductUUID string `json:"product_uuid" binding:"required"`
		Quantity    int    `json:"quantity"      binding:"required,min=1"`
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
