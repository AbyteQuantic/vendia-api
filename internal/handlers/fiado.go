package handlers

import (
	"fmt"
	"log"
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
		userID := middleware.GetUserID(c)
		branchID := middleware.GetBranchID(c)

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
				log.Printf("[init-fiado] create customer failed tenant=%s phone=%q email=%q: %v",
					tenantID, req.CustomerPhone, req.CustomerEmail, err)
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
				log.Printf("[init-fiado] merge update failed credit=%s tenant=%s: %v",
					openAcct.ID, tenantID, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al actualizar fiado existente: %v", err)})
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

		// Use a map-based insert so we omit created_by / branch_id when the
		// JWT claims are empty (legacy tokens). Postgres rejects empty
		// strings on UUID columns; omitting the field lets it default to
		// NULL. Returning the full struct afterwards requires a refetch,
		// which is still cheaper than a failed insert.
		row := map[string]any{
			"id":           uuid.NewString(),
			"tenant_id":    tenantID,
			"customer_id":  customer.ID,
			"total_amount": req.TotalAmount,
			"status":       "pending",
			"fiado_token":  token,
			"fiado_status": FiadoLinkSent,
		}
		if userID != "" {
			row["created_by"] = userID
		}
		if branchID != "" {
			row["branch_id"] = branchID
		}
		if err := db.Model(&models.CreditAccount{}).Create(row).Error; err != nil {
			log.Printf("[init-fiado] create credit failed tenant=%s customer=%s total=%d: %v",
				tenantID, customer.ID, req.TotalAmount, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al crear fiado: %v", err)})
			return
		}

		// Refetch so we hand the response builder a fully-populated struct.
		var credit models.CreditAccount
		if err := db.Where("id = ?", row["id"]).First(&credit).Error; err != nil {
			log.Printf("[init-fiado] refetch failed id=%s: %v", row["id"], err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer fiado recién creado"})
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

		// Load all sales linked to this credit — both the original (if the
		// credit has a sale_id) and every append (sales with credit_account_id
		// set). Preload items so the customer can see what they bought.
		var sales []models.Sale
		saleQuery := db.Preload("Items").Where("tenant_id = ?", credit.TenantID)
		conds := "credit_account_id = ?"
		args := []any{credit.ID}
		if credit.SaleID != nil && *credit.SaleID != "" {
			conds = "credit_account_id = ? OR id = ?"
			args = []any{credit.ID, *credit.SaleID}
		}
		saleQuery.Where(conds, args...).Order("created_at ASC").Find(&sales)

		// Timeline entry — a single struct serves both "compra" and "abono".
		type TimelineItem struct {
			Name     string  `json:"name"`
			Quantity int     `json:"quantity"`
			Price    float64 `json:"price"`
			Subtotal float64 `json:"subtotal"`
		}
		type TimelineEntry struct {
			Type          string         `json:"type"` // "debt" or "payment"
			Amount        int64          `json:"amount"`
			Note          string         `json:"note"`
			CreatedAt     string         `json:"created_at"`
			Items         []TimelineItem `json:"items,omitempty"`
			PaymentMethod string         `json:"payment_method,omitempty"`
		}
		var timeline []TimelineEntry

		// One debt entry per linked sale (with items). If no sales are
		// linked yet — e.g. legacy fiados opened via InitFiado before the
		// sale-link feature — fall back to a single summary debt entry.
		itemizedTotal := int64(0)
		for _, s := range sales {
			items := make([]TimelineItem, 0, len(s.Items))
			for _, it := range s.Items {
				items = append(items, TimelineItem{
					Name:     it.Name,
					Quantity: it.Quantity,
					Price:    it.Price,
					Subtotal: it.Subtotal,
				})
			}
			timeline = append(timeline, TimelineEntry{
				Type:      "debt",
				Amount:    int64(s.Total),
				Note:      "",
				CreatedAt: s.CreatedAt.Format("2006-01-02T15:04:05Z"),
				Items:     items,
			})
			itemizedTotal += int64(s.Total)
		}
		// Bridge legacy: if we have unexplained debt not covered by linked
		// sales, render a single summary row so the numbers still add up.
		if remainder := credit.TotalAmount - itemizedTotal; remainder > 0 {
			timeline = append(timeline, TimelineEntry{
				Type:      "debt",
				Amount:    remainder,
				Note:      credit.Description,
				CreatedAt: credit.CreatedAt.Format("2006-01-02T15:04:05Z"),
			})
		}

		for _, p := range payments {
			timeline = append(timeline, TimelineEntry{
				Type:          "payment",
				Amount:        p.Amount,
				Note:          p.Note,
				CreatedAt:     p.CreatedAt.Format("2006-01-02T15:04:05Z"),
				PaymentMethod: p.PaymentMethod,
			})
		}

		// Tenant's digital wallets (Nequi, Daviplata, Bancolombia, etc.) so
		// the customer can pay without visiting the store physically. Only
		// active methods are exposed.
		var methods []models.TenantPaymentMethod
		db.Where("tenant_id = ? AND is_active = true", credit.TenantID).
			Order("created_at ASC").
			Find(&methods)
		type PaymentOption struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Details string `json:"details"`
		}
		paymentOptions := make([]PaymentOption, 0, len(methods))
		for _, m := range methods {
			paymentOptions = append(paymentOptions, PaymentOption{
				ID:      m.ID,
				Name:    m.Name,
				Details: m.AccountDetails,
			})
		}

		balance := credit.TotalAmount - credit.PaidAmount

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name":   tenant.BusinessName,
				"business_logo":   tenant.LogoURL,
				"business_phone":  tenant.Phone,
				"customer_name":   credit.Customer.Name,
				"customer_phone":  credit.Customer.Phone,
				"total_amount":    credit.TotalAmount,
				"paid_amount":     credit.PaidAmount,
				"balance":         balance,
				"description":     credit.Description,
				"fiado_status":    credit.FiadoStatus,
				"status":          credit.Status,
				"created_at":      credit.CreatedAt,
				"accepted_at":     credit.AcceptedAt,
				"timeline":        timeline,
				"payment_methods": paymentOptions,
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
