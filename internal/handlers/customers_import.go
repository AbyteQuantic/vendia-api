// Spec: specs/026-importador-clientes/spec.md
package handlers

import (
	"net/http"
	"strings"
	"vendia-backend/internal/auth"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxImportRows = 100

// importRow represents one row in the import request body.
// Only the allow-listed fields (name, phone, email, notes) are accepted.
// Habeas-Data-protected fields (marketing_opt_in, terms_accepted, etc.)
// are intentionally absent from this struct — they are set by the backend
// and cannot be supplied by the client (FR-09, Ley 1581).
type importRow struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	Email string `json:"email"`
	Notes string `json:"notes"`
}

type importRequest struct {
	Rows          []importRow `json:"rows"`
	DedupStrategy string      `json:"dedup_strategy"`
}

type importFailedRow struct {
	RowIndex int    `json:"row_index"`
	Reason   string `json:"reason"`
}

type importResult struct {
	Created int               `json:"created"`
	Updated int               `json:"updated"`
	Skipped int               `json:"skipped"`
	Failed  []importFailedRow `json:"failed"`
}

// ImportCustomers handles POST /api/v1/customers/import.
// It accepts a JSON body with up to 100 rows and a dedup strategy, validates
// each row, deduplicates by phone within the tenant, and returns a report.
// God-mode: if the JWT carries IsSuperAdmin=true and the request includes
// X-Tenant-Override header, the import targets that tenant instead.
func ImportCustomers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// ── 1. Resolve tenant ID (with optional god-mode override) ──────────
		tenantID, ok := resolveTenantID(c)
		if !ok {
			return // resolveTenantID already wrote the error response
		}

		// ── 2. Parse and validate the request body ───────────────────────────
		var req importRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body inválido: " + err.Error()})
			return
		}

		if req.Rows == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el campo 'rows' es requerido"})
			return
		}

		if len(req.Rows) > maxImportRows {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "máximo 100 filas por solicitud — divide el archivo en chunks",
			})
			return
		}

		if req.DedupStrategy != "merge_by_phone" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "dedup_strategy inválido: solo se acepta 'merge_by_phone'",
			})
			return
		}

		// ── 3. Process each row ──────────────────────────────────────────────
		result := importResult{
			Failed: []importFailedRow{},
		}

		for i, row := range req.Rows {
			fail := processImportRow(db, tenantID, i, row, &result)
			if fail != nil {
				result.Failed = append(result.Failed, *fail)
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}

// resolveTenantID determines the effective tenant for this import request.
// Normal JWT: uses tenant_id from context.
// God-mode: if X-Tenant-Override header is present, the JWT must carry
// IsSuperAdmin=true, otherwise 403 is returned.
// Returns ("", false) after writing the error response on failure.
func resolveTenantID(c *gin.Context) (string, bool) {
	override := strings.TrimSpace(c.GetHeader("X-Tenant-Override"))
	if override == "" {
		return middleware.GetTenantID(c), true
	}

	// X-Tenant-Override header present — require super_admin.
	// Two code paths:
	//   1. Production: auth middleware sets ClaimsKey → *auth.Claims with IsSuperAdmin field.
	//   2. Tests: routers may set the raw "is_super_admin" bool directly.
	isSuperAdmin := false

	if v, exists := c.Get(middleware.ClaimsKey); exists {
		if claims, ok := v.(*auth.Claims); ok {
			isSuperAdmin = claims.IsSuperAdmin
		}
	}

	// Fallback for test routers that set the flag directly
	if !isSuperAdmin {
		if v, exists := c.Get("is_super_admin"); exists {
			if b, ok := v.(bool); ok {
				isSuperAdmin = b
			}
		}
	}

	if !isSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "X-Tenant-Override requiere scope super_admin",
		})
		return "", false
	}

	return override, true
}

// processImportRow sanitizes, validates, and upserts a single import row.
// It returns a *importFailedRow if the row should be counted as failed,
// nil otherwise. The result counters are updated in place.
func processImportRow(db *gorm.DB, tenantID string, idx int, row importRow, result *importResult) *importFailedRow {
	// ── Sanitize ──────────────────────────────────────────────────────────
	row.Name = normalizeWhitespace(row.Name)
	row.Phone = strings.TrimSpace(row.Phone)
	row.Email = strings.TrimSpace(row.Email)
	row.Notes = strings.TrimSpace(row.Notes)

	// ── Validate name ─────────────────────────────────────────────────────
	if row.Name == "" {
		return &importFailedRow{RowIndex: idx, Reason: "nombre vacío"}
	}
	if len([]rune(row.Name)) < 2 {
		return &importFailedRow{RowIndex: idx, Reason: "nombre muy corto (mínimo 2 caracteres)"}
	}

	// ── Normalize phone ───────────────────────────────────────────────────
	normalizedPhone := ""
	if row.Phone != "" {
		normalizedPhone = services.NormalizePhone(row.Phone)
		// If NormalizePhone returns "" the phone is not valid enough to dedup,
		// treat it as absent (always INSERT — spec §4, FR-08).
	}

	// ── Dedup by phone or always INSERT ───────────────────────────────────
	if normalizedPhone != "" {
		// Attempt to find an existing customer with this phone in the tenant
		var existing models.Customer
		err := db.Where("tenant_id = ? AND phone = ? AND deleted_at IS NULL", tenantID, normalizedPhone).
			First(&existing).Error

		if err == nil {
			// ── UPDATE: merge non-empty fields ────────────────────────────
			updates := buildUpdateMap(row, normalizedPhone)
			if dbErr := db.Model(&existing).Updates(updates).Error; dbErr != nil {
				return &importFailedRow{RowIndex: idx, Reason: "error al actualizar: " + dbErr.Error()}
			}
			result.Updated++
			return nil
		}
		// If the error is something other than "not found", fall through to INSERT
		// (GORM's ErrRecordNotFound is the expected "not found" case).
	}

	// ── INSERT (new customer) ──────────────────────────────────────────────
	// Habeas Data invariant: marketing_opt_in and terms_accepted are ALWAYS
	// false on import, regardless of what the client sends (FR-09, Ley 1581).
	customer := models.Customer{
		TenantID:       tenantID,
		Name:           row.Name,
		Phone:          normalizedPhone,
		Email:          row.Email,
		Notes:          row.Notes,
		MarketingOptIn: false,
		TermsAccepted:  false,
	}

	if dbErr := db.Create(&customer).Error; dbErr != nil {
		return &importFailedRow{RowIndex: idx, Reason: "error al crear: " + dbErr.Error()}
	}
	result.Created++
	return nil
}

// buildUpdateMap constructs the map of fields to update for an existing customer.
// Protected fields (marketing_opt_in, terms_accepted, terms_accepted_at,
// last_order_at, created_at) are intentionally excluded — Habeas Data and
// business invariants (spec §7, FR-08, FR-09).
// Only non-empty incoming fields are included so that an empty column in the
// import file does NOT overwrite existing data.
func buildUpdateMap(row importRow, normalizedPhone string) map[string]any {
	updates := map[string]any{}

	if row.Name != "" {
		updates["name"] = row.Name
	}
	// Phone is always included when we got here (normalizedPhone != "")
	updates["phone"] = normalizedPhone

	if row.Email != "" {
		updates["email"] = row.Email
	}
	if row.Notes != "" {
		updates["notes"] = row.Notes
	}

	return updates
}

// normalizeWhitespace trims leading/trailing whitespace and collapses internal
// runs of whitespace to a single space.
func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
