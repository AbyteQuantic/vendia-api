package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Fiado statuses
const (
	FiadoLinkSent   = "link_sent"
	FiadoLinkOpened = "link_opened"
	FiadoAccepted   = "accepted"
	FiadoRejected   = "rejected"
)

// InitFiado creates or returns an existing credit account (idempotent).
// POST /api/v1/fiado/init
func InitFiado(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		IdempotencyKey string `json:"idempotency_key"`
		CustomerName   string `json:"customer_name" binding:"required"`
		CustomerPhone  string `json:"customer_phone"`
		CustomerEmail  string `json:"customer_email"`
		TotalAmount    int64  `json:"total_amount" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.CustomerPhone == "" && req.CustomerEmail == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ingrese al menos un celular o correo"})
			return
		}

		// ── Idempotency: check if a pending fiado already exists ─────────
		if req.IdempotencyKey != "" {
			var existing models.CreditAccount
			if err := db.Where("fiado_token = ? AND tenant_id = ?", req.IdempotencyKey, tenantID).
				First(&existing).Error; err == nil {
				// Already exists — return the same data
				c.JSON(http.StatusOK, buildFiadoResponse(db, existing, tenantID))
				return
			}
		}

		// Also check for pending fiado with same customer+amount in last 5 min
		var recent models.CreditAccount
		fiveMinAgo := time.Now().Add(-5 * time.Minute)
		if err := db.Where("tenant_id = ? AND total_amount = ? AND fiado_status IN (?, ?) AND created_at > ?",
			tenantID, req.TotalAmount, FiadoLinkSent, FiadoLinkOpened, fiveMinAgo).
			Joins("JOIN customers ON customers.id = credit_accounts.customer_id AND customers.phone = ?", req.CustomerPhone).
			First(&recent).Error; err == nil {
			c.JSON(http.StatusOK, buildFiadoResponse(db, recent, tenantID))
			return
		}

		// ── Find or create customer ─────────────────────────────────────
		var customer models.Customer
		found := false
		if req.CustomerPhone != "" {
			if err := db.Where("tenant_id = ? AND phone = ?", tenantID, req.CustomerPhone).
				First(&customer).Error; err == nil {
				found = true
			}
		}
		if !found && req.CustomerEmail != "" {
			if err := db.Where("tenant_id = ? AND email = ?", tenantID, req.CustomerEmail).
				First(&customer).Error; err == nil {
				found = true
			}
		}
		if !found {
			customer = models.Customer{
				TenantID: tenantID,
				Name:     req.CustomerName,
				Phone:    req.CustomerPhone,
				Email:    req.CustomerEmail,
			}
			if err := db.Create(&customer).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al crear cliente: %v", err)})
				return
			}
		} else {
			updates := map[string]any{}
			if req.CustomerEmail != "" && customer.Email == "" {
				updates["email"] = req.CustomerEmail
			}
			if req.CustomerPhone != "" && customer.Phone == "" {
				updates["phone"] = req.CustomerPhone
			}
			if len(updates) > 0 {
				db.Model(&customer).Updates(updates)
			}
		}

		// ── Create credit account ───────────────────────────────────────
		token := req.IdempotencyKey
		if token == "" || !models.IsValidUUID(token) {
			token = uuid.NewString()
		}

		credit := models.CreditAccount{
			TenantID:    tenantID,
			CustomerID:  customer.ID,
			TotalAmount: req.TotalAmount,
			Status:      "pending",
			FiadoToken:  token,
			FiadoStatus: FiadoLinkSent,
		}

		if err := db.Create(&credit).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al crear fiado: %v", err)})
			return
		}

		c.JSON(http.StatusCreated, buildFiadoResponse(db, credit, tenantID))
	}
}

func buildFiadoResponse(db *gorm.DB, credit models.CreditAccount, tenantID string) gin.H {
	var tenant models.Tenant
	db.First(&tenant, "id = ?", tenantID)

	var customer models.Customer
	db.First(&customer, "id = ?", credit.CustomerID)

	acceptURL := fmt.Sprintf("https://vendia-admin.onrender.com/fiado/%s", credit.FiadoToken)

	resp := gin.H{
		"credit_id":      credit.ID,
		"fiado_token":    credit.FiadoToken,
		"fiado_status":   credit.FiadoStatus,
		"accept_url":     acceptURL,
		"customer_name":  customer.Name,
		"customer_phone": customer.Phone,
		"customer_email": customer.Email,
		"total_amount":   credit.TotalAmount,
	}

	if customer.Phone != "" {
		waMessage := fmt.Sprintf(
			"Hola %s, %s le ha fiado productos por $%d.\n\nAcepte los términos aquí:\n%s",
			customer.Name, tenant.BusinessName, credit.TotalAmount, acceptURL,
		)
		resp["whatsapp_url"] = fmt.Sprintf("https://wa.me/57%s?text=%s",
			customer.Phone, url.QueryEscape(waMessage))
	}

	if customer.Email != "" {
		subject := url.QueryEscape(fmt.Sprintf("Fiado en %s", tenant.BusinessName))
		body := url.QueryEscape(fmt.Sprintf(
			"Hola %s,\n\n%s le ha fiado productos por $%d.\n\nAcepte aquí: %s",
			customer.Name, tenant.BusinessName, credit.TotalAmount, acceptURL,
		))
		resp["email_url"] = fmt.Sprintf("mailto:%s?subject=%s&body=%s",
			customer.Email, subject, body)
	}

	return gin.H{"data": resp}
}

// GetFiadoPublic returns fiado details for the public acceptance page.
// GET /api/v1/public/fiado/:token
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

		// Mark as opened if still in link_sent
		if credit.FiadoStatus == FiadoLinkSent {
			db.Model(&credit).Update("fiado_status", FiadoLinkOpened)
			credit.FiadoStatus = FiadoLinkOpened
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name":  tenant.BusinessName,
				"business_logo":  tenant.LogoURL,
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
// POST /api/v1/public/fiado/:token/accept
func AcceptFiado(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PhoneConfirm string `json:"phone_confirm" binding:"required"`
		AcceptTerms  bool   `json:"accept_terms"`
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

		if credit.FiadoStatus == FiadoAccepted {
			c.JSON(http.StatusOK, gin.H{"message": "ya fue aceptado"})
			return
		}

		if credit.Customer.Phone != req.PhoneConfirm {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el teléfono no coincide"})
			return
		}

		now := time.Now()
		db.Model(&credit).Updates(map[string]any{
			"fiado_status": FiadoAccepted,
			"status":       "open",
			"accepted_at":  now,
			"accepted_ip":  c.ClientIP(),
		})

		c.JSON(http.StatusOK, gin.H{
			"message": "fiado aceptado",
			"data":    gin.H{"fiado_status": FiadoAccepted, "accepted_at": now},
		})
	}
}

// CheckFiadoStatus returns current status (polling from Flutter).
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
