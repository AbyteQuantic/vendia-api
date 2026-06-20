// Spec: specs/075-proveedores-b2b/spec.md
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func isSupplierTenant(t models.Tenant) bool {
	for _, bt := range t.BusinessTypes {
		if bt == models.BusinessTypeProveedorAgricola || bt == models.BusinessTypeProveedorMayorista {
			return true
		}
	}
	return false
}

// SupplierCatalog — GET /api/v1/suppliers/:id/catalog
// La tienda (buyer) ve el catálogo PÚBLICO de un proveedor (cross-tenant, solo
// lectura — Art. III). Campos públicos del producto, nada privado del tenant.
func SupplierCatalog(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var supplier models.Tenant
		if err := db.Where("id = ?", c.Param("uuid")).First(&supplier).Error; err != nil || !isSupplierTenant(supplier) {
			c.JSON(http.StatusNotFound, gin.H{"error": "proveedor no encontrado"})
			return
		}
		type pubProduct struct {
			ID         string  `json:"id"`
			Name       string  `json:"name"`
			Price      float64 `json:"price"`
			Stock      float64 `json:"stock"`
			Category   string  `json:"category"`
			ExpiryDate *string `json:"expiry_date,omitempty"`
			PhotoURL   string  `json:"photo_url"`
		}
		var products []pubProduct
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND deleted_at IS NULL", supplier.ID).
			Select("id, name, price, stock, category, expiry_date, photo_url").
			Scan(&products)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"supplier": gin.H{
				"id":             supplier.ID,
				"business_name":  supplier.BusinessName,
				"business_types": supplier.BusinessTypes,
				"address":        supplier.Address,
			},
			"products": products,
		}})
	}
}

type orderItemReq struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	Quantity  float64 `json:"quantity"`
	Price     float64 `json:"price"`
}

// PlaceSupplierOrder — POST /api/v1/suppliers/:id/orders
// La tienda hace un pedido al proveedor. VendIA registra la intención (GMV) y
// devuelve un link de WhatsApp para cerrarlo (no procesa el pago).
func PlaceSupplierOrder(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		buyerID := middleware.GetTenantID(c)

		var supplier models.Tenant
		if err := db.Where("id = ?", c.Param("uuid")).First(&supplier).Error; err != nil || !isSupplierTenant(supplier) {
			c.JSON(http.StatusNotFound, gin.H{"error": "proveedor no encontrado"})
			return
		}
		var buyer models.Tenant
		if err := db.Where("id = ?", buyerID).First(&buyer).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var req struct {
			Items          []orderItemReq `json:"items"`
			DeliveryChoice string         `json:"delivery_choice"`
			Notes          string         `json:"notes"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "El pedido no tiene productos."})
			return
		}
		delivery := req.DeliveryChoice
		switch delivery {
		case models.DeliveryProveedorEntrega, models.DeliveryTiendaRecoge, models.DeliveryPorAcordar:
		default:
			delivery = models.DeliveryPorAcordar
		}

		var total float64
		for _, it := range req.Items {
			total += it.Quantity * it.Price
		}
		itemsJSON, _ := json.Marshal(req.Items)

		order := models.SupplierOrder{
			SupplierTenantID: supplier.ID,
			BuyerTenantID:    buyer.ID,
			BuyerName:        buyer.BusinessName,
			BuyerPhone:       buyer.Phone,
			Items:            string(itemsJSON),
			TotalAmount:      total,
			DeliveryChoice:   delivery,
			Status:           models.SupplierOrderNuevo,
			Notes:            req.Notes,
		}
		if err := db.Create(&order).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el pedido"})
			return
		}

		// Mensaje de WhatsApp para cerrar el pedido con el proveedor.
		waSvc := services.NewWhatsAppService()
		msg := buildSupplierOrderMessage(buyer.BusinessName, req.Items, total, delivery)
		waURL := waSvc.BuildURL(supplier.Phone, msg)

		c.JSON(http.StatusCreated, gin.H{"data": gin.H{
			"order":        order,
			"whatsapp_url": waURL,
			"message":      msg,
		}})
	}
}

func deliveryLabel(choice string) string {
	switch choice {
	case models.DeliveryProveedorEntrega:
		return "El proveedor lleva"
	case models.DeliveryTiendaRecoge:
		return "Yo recojo"
	default:
		return "Lo acordamos"
	}
}

func buildSupplierOrderMessage(buyerName string, items []orderItemReq, total float64, delivery string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hola, soy %s. Quiero hacer un pedido:\n", buyerName)
	for _, it := range items {
		fmt.Fprintf(&b, "• %s x%g\n", it.Name, it.Quantity)
	}
	fmt.Fprintf(&b, "Total aprox: $%.0f\n", total)
	fmt.Fprintf(&b, "Entrega: %s\n¿Me confirma disponibilidad?", deliveryLabel(delivery))
	return b.String()
}

// SupplierInbox — GET /api/v1/supplier/inbox
// El proveedor ve los pedidos entrantes (donde él es el proveedor).
func SupplierInbox(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		me := middleware.GetTenantID(c)
		var orders []models.SupplierOrder
		db.Where("supplier_tenant_id = ?", me).Order("created_at DESC").Find(&orders)
		c.JSON(http.StatusOK, gin.H{"data": orders})
	}
}

// UpdateSupplierOrderStatus — PATCH /api/v1/supplier/orders/:orderId
// El proveedor cambia el estado de un pedido entrante suyo.
func UpdateSupplierOrderStatus(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		me := middleware.GetTenantID(c)
		var req struct {
			Status string `json:"status"`
		}
		_ = c.ShouldBindJSON(&req)
		switch req.Status {
		case models.SupplierOrderConfirmado, models.SupplierOrderEntregado, models.SupplierOrderCancelado, models.SupplierOrderNuevo:
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "estado inválido"})
			return
		}
		res := db.Model(&models.SupplierOrder{}).
			Where("id = ? AND supplier_tenant_id = ?", c.Param("orderId"), me).
			Update("status", req.Status)
		if res.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "pedido no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "estado actualizado"})
	}
}
