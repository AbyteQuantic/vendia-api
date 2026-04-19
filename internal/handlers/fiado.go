package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

		// ── One-Open-Account rule: if this customer already has an
		// accepted (open) fiado, bump its total and return the SAME
		// credit_id + fiado_token (no new handshake needed).
		// Only accepted accounts are merged — pending/link_sent are not.
		var openAcct models.CreditAccount
		if err := db.Where(
			"tenant_id = ? AND customer_id = ? AND status = ? AND fiado_status = ?",
			tenantID, customer.ID, "open", FiadoAccepted,
		).First(&openAcct).Error; err == nil {
			newTotal := openAcct.TotalAmount + req.TotalAmount
			if err := db.Model(&openAcct).Update("total_amount", newTotal).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar fiado existente"})
				return
			}
			openAcct.TotalAmount = newTotal
			resp := buildFiadoResponse(db, openAcct, tenantID)
			// Mark as merged so the client knows to skip the WhatsApp handshake.
			if data, ok := resp["data"].(gin.H); ok {
				data["merged"] = true
				data["added_amount"] = req.TotalAmount
			}
			c.JSON(http.StatusOK, resp)
			return
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

// AppendToFiado adds an amount to an existing, already-accepted (open) credit
// account. No handshake, no new token — the owner already authorized this
// line of credit. Used when the cashier picks "Agregar a esta cuenta" from
// the Cuaderno detail screen.
// POST /api/v1/fiado/:id/append
func AppendToFiado(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		TotalAmount int64  `json:"total_amount" binding:"required,gt=0"`
		Note        string `json:"note"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		creditID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var credit models.CreditAccount
		if err := db.Where("id = ? AND tenant_id = ?", creditID, tenantID).
			First(&credit).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "fiado no encontrado"})
			return
		}
		if credit.Status != "open" || credit.FiadoStatus != FiadoAccepted {
			c.JSON(http.StatusConflict, gin.H{
				"error": "la cuenta no está abierta o aún no fue aceptada por el cliente",
			})
			return
		}

		newTotal := credit.TotalAmount + req.TotalAmount
		if err := db.Model(&credit).Update("total_amount", newTotal).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar fiado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"credit_id":    credit.ID,
				"total_amount": newTotal,
				"added_amount": req.TotalAmount,
				"balance":      newTotal - credit.PaidAmount,
			},
		})
	}
}

func buildFiadoResponse(db *gorm.DB, credit models.CreditAccount, tenantID string) gin.H {
	var tenant models.Tenant
	db.First(&tenant, "id = ?", tenantID)

	var customer models.Customer
	db.First(&customer, "id = ?", credit.CustomerID)

	acceptURL := fmt.Sprintf("https://vendia-admin.vercel.app/fiado/%s", credit.FiadoToken)

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
		subject := "Fiado en " + tenant.BusinessName
		body := fmt.Sprintf(
			"Hola %s,\r\n\r\n%s le ha fiado productos por $%d.\r\n\r\nAcepte aquí: %s",
			customer.Name, tenant.BusinessName, credit.TotalAmount, acceptURL,
		)
		// Use url.PathEscape for spaces (not QueryEscape which uses +)
		resp["email_url"] = fmt.Sprintf("mailto:%s?subject=%s&body=%s",
			customer.Email,
			strings.ReplaceAll(url.QueryEscape(subject), "+", "%20"),
			strings.ReplaceAll(url.QueryEscape(body), "+", "%20"))
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

		// Load payments for timeline
		var payments []models.CreditPayment
		db.Where("credit_account_id = ?", credit.ID).Order("created_at ASC").Find(&payments)

		// Build timeline
		type TimelineEntry struct {
			Type      string `json:"type"` // debt or payment
			Amount    int64  `json:"amount"`
			Note      string `json:"note"`
			CreatedAt string `json:"created_at"`
		}
		var timeline []TimelineEntry

		// Initial debt
		timeline = append(timeline, TimelineEntry{
			Type:      "debt",
			Amount:    credit.TotalAmount,
			Note:      credit.Description,
			CreatedAt: credit.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})

		for _, p := range payments {
			timeline = append(timeline, TimelineEntry{
				Type:      "payment",
				Amount:    p.Amount,
				Note:      p.Note,
				CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z"),
			})
		}

		balance := credit.TotalAmount - credit.PaidAmount

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name":  tenant.BusinessName,
				"business_logo":  tenant.LogoURL,
				"customer_name":  credit.Customer.Name,
				"customer_phone": credit.Customer.Phone,
				"total_amount":   credit.TotalAmount,
				"paid_amount":    credit.PaidAmount,
				"balance":        balance,
				"description":    credit.Description,
				"fiado_status":   credit.FiadoStatus,
				"status":         credit.Status,
				"created_at":     credit.CreatedAt,
				"accepted_at":    credit.AcceptedAt,
				"timeline":       timeline,
			},
		})
	}
}

// AcceptFiado marks a fiado as accepted by the customer.
// POST /api/v1/public/fiado/:token/accept
func AcceptFiado(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PhoneConfirm string `json:"phone_confirm"`
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

		// Verify phone only if customer has one registered
		if credit.Customer.Phone != "" && req.PhoneConfirm != credit.Customer.Phone {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el teléfono no coincide"})
			return
		}

		// If customer had no phone and client provides one, save it
		if credit.Customer.Phone == "" && req.PhoneConfirm != "" {
			db.Model(&credit.Customer).Update("phone", req.PhoneConfirm)
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
