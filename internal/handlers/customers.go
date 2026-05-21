// Spec: specs/030-administracion-clientes-no-tienda/spec.md
package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// customerListLimits mirrors the plan §4 contract for GET /api/v1/customers:
// default 50, hard cap 200. Kept separate from the shared parsePagination
// (which caps at 100 / uses page+per_page) because the F030 "Mis clientes"
// screen pages with limit/offset and a larger ceiling.
const (
	customerDefaultLimit = 50
	customerMaxLimit     = 200
)

// customerHistoryDefaultLimit is the page size for the sales timeline in
// GET /api/v1/customers/:id/history. The summary aggregates the FULL
// history regardless of this page; only the `sales` array is paged.
const customerHistoryDefaultLimit = 50

// customerOffsetLimit parses the F030 limit/offset query params. Invalid or
// missing values fall back to the defaults; limit is clamped to [1, max].
func customerOffsetLimit(c *gin.Context, defaultLimit int) (limit, offset int) {
	limit = defaultLimit
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > customerMaxLimit {
		limit = customerMaxLimit
	}
	offset = 0
	if raw := c.Query("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			offset = v
		}
	}
	return limit, offset
}

// customerAggregate is the row shape returned by ListCustomers — the
// Customer columns plus the three per-customer aggregates computed from
// sales via a LEFT JOIN. last_purchase_at is nullable (a customer created
// by the F026 importer or on-the-fly in checkout may have zero sales).
type customerAggregate struct {
	models.Customer
	TotalSpent     float64 `json:"total_spent"`
	PurchaseCount  int64   `json:"purchase_count"`
	LastPurchaseAt *string `json:"last_purchase_at"`
}

// ListCustomers returns the tenant's customers with per-customer sales
// aggregates (Spec F030). Query params:
//   - q       — case-insensitive substring match on name OR phone.
//   - limit   — page size, default 50, max 200.
//   - offset  — rows to skip, default 0.
//
// The aggregates (total_spent, purchase_count, last_purchase_at) are
// computed in the query with a LEFT JOIN to sales scoped to the same
// tenant; anonymous sales never enter because the join is on customer_id.
// GET /api/v1/customers
func ListCustomers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		limit, offset := customerOffsetLimit(c, customerDefaultLimit)
		q := strings.TrimSpace(c.Query("q"))

		// base filters the customers table by tenant; the optional `q`
		// term matches name OR phone, case-insensitively. LOWER + LIKE is
		// used (instead of Postgres-only ILIKE) so the same query runs on
		// the SQLite test driver.
		base := db.Model(&models.Customer{}).Where("customers.tenant_id = ?", tenantID)
		if q != "" {
			like := "%" + strings.ToLower(q) + "%"
			base = base.Where(
				"LOWER(customers.name) LIKE ? OR LOWER(customers.phone) LIKE ?",
				like, like,
			)
		}

		var total int64
		if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al contar clientes"})
			return
		}

		var rows []customerAggregate
		// The LEFT JOIN keeps customers with zero sales (count 0, total 0,
		// last_purchase_at NULL). sales.deleted_at IS NULL excludes
		// soft-deleted sales from the aggregates. The join is scoped to
		// the tenant on both sides as a defence-in-depth measure.
		err := base.Session(&gorm.Session{}).
			Select(`customers.*,
				COALESCE(SUM(sales.total), 0) AS total_spent,
				COUNT(sales.id) AS purchase_count,
				MAX(sales.created_at) AS last_purchase_at`).
			Joins(`LEFT JOIN sales ON sales.customer_id = customers.id
				AND sales.tenant_id = customers.tenant_id
				AND sales.deleted_at IS NULL`).
			Group("customers.id").
			Order("customers.name ASC").
			Limit(limit).
			Offset(offset).
			Find(&rows).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener clientes"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": rows,
			"meta": gin.H{
				"total":  total,
				"limit":  limit,
				"offset": offset,
			},
		})
	}
}

// GetCustomerHistory returns a single customer plus a summary of their
// purchase history and a paginated list of their sales (Spec F030 AC-06).
// A customer id that does not belong to the caller's tenant returns 404 —
// the same opaque response a non-existent id gets, so the endpoint never
// leaks the existence of another tenant's customer.
// GET /api/v1/customers/:id/history
func GetCustomerHistory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		customerID := c.Param("id")

		// Tenant-scoped lookup: a cross-tenant id is indistinguishable
		// from a missing one (Constitución Art. III + VI).
		var customer models.Customer
		if err := db.Where("id = ? AND tenant_id = ?", customerID, tenantID).
			First(&customer).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cliente no encontrado"})
			return
		}

		// Summary aggregates the customer's ENTIRE sales history,
		// independent of the sales-list pagination below.
		type summaryRow struct {
			TotalSpent      float64 `json:"total_spent"`
			PurchaseCount   int64   `json:"purchase_count"`
			LastPurchaseAt  *string `json:"last_purchase_at"`
			FirstPurchaseAt *string `json:"first_purchase_at"`
		}
		var summary summaryRow
		if err := db.Model(&models.Sale{}).
			Select(`COALESCE(SUM(total), 0) AS total_spent,
				COUNT(id) AS purchase_count,
				MAX(created_at) AS last_purchase_at,
				MIN(created_at) AS first_purchase_at`).
			Where("tenant_id = ? AND customer_id = ?", tenantID, customerID).
			Scan(&summary).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al calcular el resumen"})
			return
		}

		// Paged sales timeline, newest first. Items preloaded so the
		// detail screen can show each ticket's line count without a
		// second round-trip.
		limit, offset := customerOffsetLimit(c, customerHistoryDefaultLimit)
		var sales []models.Sale
		if err := db.Preload("Items").
			Where("tenant_id = ? AND customer_id = ?", tenantID, customerID).
			Order("created_at DESC").
			Limit(limit).
			Offset(offset).
			Find(&sales).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener el historial"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"customer": customer,
				"summary":  summary,
				"sales":    sales,
				"meta": gin.H{
					"total":  summary.PurchaseCount,
					"limit":  limit,
					"offset": offset,
				},
			},
		})
	}
}

func CreateCustomer(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID    string `json:"id"`
		Name  string `json:"name"  binding:"required,min=2"`
		Phone string `json:"phone"`
		Notes string `json:"notes"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a valid UUID v4"})
			return
		}

		customer := models.Customer{
			TenantID: tenantID,
			Name:     req.Name,
			Phone:    req.Phone,
			Notes:    req.Notes,
		}
		if req.ID != "" {
			customer.ID = req.ID
		}

		if err := db.Create(&customer).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear cliente"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": customer})
	}
}

func UpdateCustomer(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name  *string `json:"name"`
		Phone *string `json:"phone"`
		Notes *string `json:"notes"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		customerID := c.Param("id")

		var customer models.Customer
		if err := db.Where("id = ? AND tenant_id = ?", customerID, tenantID).
			First(&customer).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cliente no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Name != nil {
			updates["name"] = *req.Name
		}
		if req.Phone != nil {
			updates["phone"] = *req.Phone
		}
		if req.Notes != nil {
			updates["notes"] = *req.Notes
		}

		if err := db.Model(&customer).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar cliente"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": customer})
	}
}
