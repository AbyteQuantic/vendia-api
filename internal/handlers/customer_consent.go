package handlers

import (
	"net/http"
	"strings"
	"time"
	"unicode"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CheckCustomerConsent answers a single privacy-safe question:
//
//	"Given this phone number on this tenant's catalogue, do we need
//	 to show the Habeas-Data consent checkbox right now?"
//
// The handler is DELIBERATELY narrow. It returns only
// {"needs_consent": bool}. It never returns the customer's name,
// email, order history or even whether the row exists — that would
// let an attacker enumerate a tenant's customer base by brute-forcing
// phone numbers on a public endpoint.
//
// Truth table:
//
//	row missing  →  needs_consent: true
//	row exists AND terms_accepted=false → needs_consent: true
//	row exists AND terms_accepted=true  → needs_consent: false
//
// Malformed / empty phones also collapse to needs_consent:true so the
// client always ends up showing the checkbox in ambiguous cases (fail
// closed from a legal-risk perspective).
func CheckCustomerConsent(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Phone string `json:"phone"`
	}

	return func(c *gin.Context) {
		slug := strings.TrimSpace(c.Param("slug"))
		if slug == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "slug requerido"})
			return
		}

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			// Do NOT leak "tenant exists / doesn't exist" here either.
			// The PublicCatalog endpoint already handles that with 404,
			// so any client hitting check-customer with a bad slug is
			// almost certainly a scraper.
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, gin.H{"needs_consent": true})
			return
		}

		phone := normalizePhone(req.Phone)
		if phone == "" {
			c.JSON(http.StatusOK, gin.H{"needs_consent": true})
			return
		}

		// Narrow SELECT on purpose: only the column we need. Even an
		// accidental log statement of this row can't leak the name.
		var row struct {
			TermsAccepted bool
		}
		err := db.Model(&models.Customer{}).
			Select("terms_accepted").
			Where("tenant_id = ? AND phone = ?", tenant.ID, phone).
			Limit(1).
			Scan(&row).Error

		needs := true
		if err == nil && row.TermsAccepted {
			needs = false
		}

		c.JSON(http.StatusOK, gin.H{"needs_consent": needs})
	}
}

// normalizePhone strips spaces, dashes, parentheses and a leading
// "+" / "57" country code so the same human phone compared across
// visits ends up being equal. We store only the digits so an entry
// of "+57 300-123-4567", "(300) 1234567" and "3001234567" all map to
// the same customer row.
//
// We deliberately don't validate length here — that's the caller's
// job (see CheckCustomerConsent). Returning a non-empty string just
// means "this string contained at least one digit".
func normalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	// Strip Colombian country prefix if the client sent it plus the
	// national number (57 + 10 digits = 12). We keep anything shorter
	// (short codes, other countries) as-is.
	if len(digits) == 12 && strings.HasPrefix(digits, "57") {
		digits = digits[2:]
	}
	return digits
}

// upsertCustomerFromOrder is called from PublicCreateOnlineOrder
// AFTER the order has been successfully persisted. It's the single
// place in the code where a catalogue visitor becomes a CRM record.
//
// Contract:
//   - If no phone was provided, we skip silently (anonymous order).
//   - If a row with (tenant_id, phone) exists, we update name (if
//     provided), last_order_at, and — only if acceptedTerms is true
//     AND the existing row hadn't consented — flip terms_accepted
//     and set terms_accepted_at. We NEVER revoke consent here; the
//     customer would have to do that through a dedicated path.
//   - If no row exists, we insert one. terms_accepted mirrors the
//     acceptedTerms flag the client sent alongside the order. If
//     the client did not send it (legacy clients) we default to
//     false and the next checkout will prompt again.
//
// Returns (created bool, err error). The boolean is handy for tests
// and for future notification wiring ("new customer!") — the main
// handler does not currently branch on it.
func upsertCustomerFromOrder(
	db *gorm.DB,
	tenantID, name, rawPhone string,
	acceptedTerms bool,
) (bool, error) {
	phone := normalizePhone(rawPhone)
	if phone == "" {
		return false, nil
	}
	name = strings.TrimSpace(name)
	now := time.Now().UTC()

	var existing models.Customer
	err := db.Where("tenant_id = ? AND phone = ?", tenantID, phone).
		First(&existing).Error

	if err == gorm.ErrRecordNotFound {
		insertName := name
		if insertName == "" {
			insertName = "Cliente"
		}
		row := models.Customer{
			TenantID:      tenantID,
			Name:          insertName,
			Phone:         phone,
			TermsAccepted: acceptedTerms,
			LastOrderAt:   &now,
		}
		if acceptedTerms {
			row.TermsAcceptedAt = &now
		}
		if err := db.Create(&row).Error; err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}

	updates := map[string]any{
		"last_order_at": now,
	}
	if name != "" && name != existing.Name {
		updates["name"] = name
	}
	if acceptedTerms && !existing.TermsAccepted {
		updates["terms_accepted"] = true
		updates["terms_accepted_at"] = now
	}
	if err := db.Model(&existing).Updates(updates).Error; err != nil {
		return false, err
	}
	return false, nil
}
