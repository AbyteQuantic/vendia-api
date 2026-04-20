package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GenerateDynamicQR builds a "Plan B" zero-fee payment payload for the
// current tenant and amount. No payment gateway, no merchant fees — the
// QR just carries enough for the customer's wallet app to open with the
// destination + amount pre-filled (locked). The tendero confirms receipt
// manually after the Nequi/Daviplata SMS arrives.
//
// POST /api/v1/payments/generate-dynamic-qr
// Body: {amount:int, payment_method_id?:uuid}
// Response: {qr_string, account_number, account_holder, wallet_name,
//            wallet_type, amount, locked:true, instructions}
func GenerateDynamicQR(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Amount          int64  `json:"amount"             binding:"required,gt=0"`
		PaymentMethodID string `json:"payment_method_id"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Find the method the cashier picked, or fall back to the first
		// active configured method for this tenant.
		var method models.TenantPaymentMethod
		query := db.Where("tenant_id = ? AND is_active = true", tenantID)
		if req.PaymentMethodID != "" {
			query = query.Where("id = ?", req.PaymentMethodID)
		}
		if err := query.Order("created_at ASC").First(&method).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "configure primero un método de pago digital (Nequi, Daviplata o Bancolombia) en la pestaña Mi Negocio",
			})
			return
		}

		// Pull the tenant's display name for the QR landing page.
		var tenant models.Tenant
		db.Select("id, business_name, owner_name, phone").
			Where("id = ?", tenantID).First(&tenant)

		walletType := classifyWallet(method.Name)
		accountNumber := strings.TrimSpace(method.AccountDetails)
		holderName := firstNonEmpty(tenant.OwnerName, tenant.BusinessName)

		payload := buildQRPayload(walletType, accountNumber, req.Amount, tenantID)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"qr_string":      payload,
				"account_number": accountNumber,
				"account_holder": holderName,
				"wallet_name":    method.Name,
				"wallet_type":    walletType,
				"amount":         req.Amount,
				"locked":         true,
				"instructions":   qrInstructions(walletType),
			},
		})
	}
}

// classifyWallet maps a free-text method name ("Nequi", "nequi 3001234567",
// "Bancolombia Ahorros", …) onto a canonical wallet type the client can
// branch on for icons and copy.
func classifyWallet(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nequi"):
		return "nequi"
	case strings.Contains(n, "daviplata"):
		return "daviplata"
	case strings.Contains(n, "bancolombia"):
		return "bancolombia"
	case strings.Contains(n, "davivienda"):
		return "davivienda"
	case strings.Contains(n, "bbva"):
		return "bbva"
	case strings.Contains(n, "qr"):
		return "generic_qr"
	default:
		return "transfer"
	}
}

// buildQRPayload returns the exact string encoded into the QR. For wallets
// that expose a deep-link format (Nequi, Daviplata) we use that; for
// banks we emit a self-hosted landing URL that collects the metadata
// and shows the customer the account info + copy buttons. The landing
// URL also works as a fallback for wallets — scanning with any camera
// opens the webpage.
func buildQRPayload(walletType, account string, amount int64, tenantID string) string {
	ref := tenantID
	switch walletType {
	case "nequi":
		// Best-effort Nequi deep link (unofficial but widely recognized
		// by the app when opened from a browser that supports custom
		// schemes). The "locked=true" flag signals our landing page to
		// disable the amount field.
		return fmt.Sprintf(
			"nequi://pay?phone=%s&amount=%d&ref=%s&locked=true",
			escape(account), amount, escape(ref))
	case "daviplata":
		return fmt.Sprintf(
			"daviplata://pay?phone=%s&amount=%d&ref=%s&locked=true",
			escape(account), amount, escape(ref))
	default:
		// Bancolombia / Davivienda / BBVA / unknown — no universal deep
		// link. Emit a URL pointing to our own micro-payment landing on
		// the admin web. The page shows account + amount + copy button
		// and attempts deep-links to the bank apps on tap.
		return fmt.Sprintf(
			"https://vendia-admin.vercel.app/pay?tenant=%s&account=%s&amount=%d&type=%s",
			escape(tenantID), escape(account), amount, escape(walletType))
	}
}

func escape(s string) string {
	// URL-safe-ish: whitespace stripped, basic chars preserved. We keep
	// this dependency-free to match the project's "no extra deps" posture.
	b := strings.Builder{}
	for _, r := range s {
		switch {
		case r == ' ', r == '\n', r == '\t':
			continue
		case r == '"', r == '\'', r == '&', r == '?', r == '#', r == '=':
			// These would break the URL shape — replace with safe chars.
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func qrInstructions(walletType string) string {
	switch walletType {
	case "nequi":
		return "El cliente abre su app Nequi, toca \"Enviar\" y escanea el QR. El monto viene bloqueado."
	case "daviplata":
		return "El cliente abre su app DaviPlata y escanea el QR para transferir el monto exacto."
	case "bancolombia", "davivienda", "bbva":
		return "El cliente escanea el QR con la cámara del celular; se abre la página con los datos de la cuenta para transferir."
	default:
		return "El cliente escanea el QR y completa la transferencia con los datos mostrados."
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
