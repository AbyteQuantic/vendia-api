package handlers

import (
	"fmt"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// InitFiado creates a credit account in PENDING_SIGNATURE status and returns a token.
// POST /api/v1/fiado/init
func InitFiado(db *gorm.DB) gin.HandlerFunc {
	type ItemReq struct {
		Name     string  `json:"name"`
		Quantity int     `json:"quantity"`
		Price    float64 `json:"price"`
	}
	type Request struct {
		CustomerName  string    `json:"customer_name" binding:"required"`
		CustomerPhone string    `json:"customer_phone" binding:"required"`
		TotalAmount   int64     `json:"total_amount" binding:"required"`
		Items         []ItemReq `json:"items"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Find or create customer
		var customer models.Customer
		if err := db.Where("tenant_id = ? AND phone = ?", tenantID, req.CustomerPhone).
			First(&customer).Error; err != nil {
			customer = models.Customer{
				TenantID: tenantID,
				Name:     req.CustomerName,
				Phone:    req.CustomerPhone,
			}
			db.Create(&customer)
		}

		// Create credit account with pending signature
		token := uuid.NewString()
		credit := models.CreditAccount{
			TenantID:    tenantID,
			CustomerID:  customer.ID,
			TotalAmount: req.TotalAmount,
			Status:      "pending",
			FiadoToken:  token,
			FiadoStatus: "pending_signature",
		}

		if err := db.Create(&credit).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear fiado"})
			return
		}

		// Build WhatsApp link for the customer
		var tenant models.Tenant
		db.First(&tenant, "id = ?", tenantID)

		// Summary for the message
		summary := fmt.Sprintf("Deuda de $%d en %s", req.TotalAmount, tenant.BusinessName)
		acceptURL := fmt.Sprintf("https://vendia-admin.onrender.com/fiado/%s", token)
		waMessage := fmt.Sprintf(
			"Hola %s, %s le ha fiado productos por $%d.\n\nPor favor acepte los términos aquí:\n%s",
			req.CustomerName, tenant.BusinessName, req.TotalAmount, acceptURL,
		)
		waLink := fmt.Sprintf("https://wa.me/57%s?text=%s",
			req.CustomerPhone, waMessage)

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"credit_id":    credit.ID,
				"fiado_token":  token,
				"fiado_status": credit.FiadoStatus,
				"accept_url":   acceptURL,
				"whatsapp_url": waLink,
				"summary":      summary,
			},
		})
	}
}

// GetFiadoPublic returns the fiado details for the public acceptance page.
// GET /api/v1/public/fiado/:token (no auth)
func GetFiadoPublic(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Param("token")

		var credit models.CreditAccount
		if err := db.Preload("Customer").
			Where("fiado_token = ?", token).
			First(&credit).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "fiado no encontrado"})
			return
		}

		var tenant models.Tenant
		db.First(&tenant, "id = ?", credit.TenantID)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name":  tenant.BusinessName,
				"customer_name":  credit.Customer.Name,
				"customer_phone": credit.Customer.Phone,
				"total_amount":   credit.TotalAmount,
				"fiado_status":   credit.FiadoStatus,
				"created_at":     credit.CreatedAt,
			},
		})
	}
}

// AcceptFiado marks a fiado as accepted by the customer.
// POST /api/v1/public/fiado/:token/accept (no auth)
func AcceptFiado(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PhoneConfirm string `json:"phone_confirm" binding:"required"`
		AcceptTerms  bool   `json:"accept_terms" binding:"required"`
	}

	return func(c *gin.Context) {
		token := c.Param("token")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if !req.AcceptTerms {
			c.JSON(http.StatusBadRequest, gin.H{"error": "debe aceptar los términos"})
			return
		}

		var credit models.CreditAccount
		if err := db.Preload("Customer").
			Where("fiado_token = ?", token).
			First(&credit).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "fiado no encontrado"})
			return
		}

		if credit.FiadoStatus == "accepted" {
			c.JSON(http.StatusOK, gin.H{"message": "ya fue aceptado anteriormente"})
			return
		}

		if credit.Customer.Phone != req.PhoneConfirm {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el teléfono no coincide"})
			return
		}

		now := time.Now()
		ip := c.ClientIP()
		db.Model(&credit).Updates(map[string]any{
			"fiado_status": "accepted",
			"status":       "open",
			"accepted_at":  now,
			"accepted_ip":  ip,
		})

		c.JSON(http.StatusOK, gin.H{
			"message": "fiado aceptado exitosamente",
			"data": gin.H{
				"fiado_status": "accepted",
				"accepted_at":  now,
			},
		})
	}
}

// CheckFiadoStatus returns current status of a fiado (for polling from Flutter).
// GET /api/v1/fiado/:token/status
func CheckFiadoStatus(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Param("token")

		var credit models.CreditAccount
		if err := db.Where("fiado_token = ?", token).First(&credit).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "fiado no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"fiado_status": credit.FiadoStatus,
				"accepted_at":  credit.AcceptedAt,
			},
		})
	}
}
