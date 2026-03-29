package handlers

import (
	"fmt"
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func SendReceipt(db *gorm.DB) gin.HandlerFunc {
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

		if sale.CustomerID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la venta no tiene cliente asociado"})
			return
		}

		var customer models.Customer
		if err := db.Where("id = ?", *sale.CustomerID).First(&customer).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cliente no encontrado"})
			return
		}

		var tenant models.Tenant
		db.Where("id = ?", tenantID).First(&tenant)

		waSvc := services.NewWhatsAppService()
		receiptURL := fmt.Sprintf("https://vendia.co/receipt/%s", sale.ID)
		message := waSvc.ReceiptMessage(tenant.BusinessName, sale.Total, receiptURL)
		waURL := waSvc.BuildURL(customer.Phone, message)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"whatsapp_url": waURL,
				"message":      message,
			},
		})
	}
}

func RemindCredit(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		customerUUID := c.Param("customer_uuid")

		var customer models.Customer
		if err := db.Where("id = ? AND tenant_id = ?", customerUUID, tenantID).
			First(&customer).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cliente no encontrado"})
			return
		}

		var totalDebt float64
		db.Model(&models.CreditAccount{}).
			Where("tenant_id = ? AND customer_id = ? AND status IN ('open', 'partial')", tenantID, customerUUID).
			Select("COALESCE(SUM(total_amount - paid_amount), 0)").
			Scan(&totalDebt)

		if totalDebt == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el cliente no tiene deuda pendiente"})
			return
		}

		var tenant models.Tenant
		db.Where("id = ?", tenantID).First(&tenant)

		waSvc := services.NewWhatsAppService()
		message := waSvc.CreditReminder(customer.Name, tenant.BusinessName, totalDebt)
		waURL := waSvc.BuildURL(customer.Phone, message)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"whatsapp_url": waURL,
				"message":      message,
				"total_debt":   totalDebt,
			},
		})
	}
}

func GeneratePaymentQR(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		amount := c.Query("amount")
		method := c.Query("method")

		if amount == "" || method == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "amount y method son requeridos"})
			return
		}

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		var phone string
		switch method {
		case "nequi":
			if tenant.NequiPhone != nil {
				phone = *tenant.NequiPhone
			}
		case "daviplata":
			if tenant.DaviplataPhone != nil {
				phone = *tenant.DaviplataPhone
			}
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "método no soportado (nequi o daviplata)"})
			return
		}

		if phone == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no hay número configurado para " + method})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"method":  method,
				"phone":   phone,
				"amount":  amount,
				"qr_data": fmt.Sprintf("%s:%s:%s", method, phone, amount),
			},
		})
	}
}
