package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// LogInvoiceSave records a completed invoice save for the owner's audit trail.
// POST /api/v1/inventory/invoice-logs
func LogInvoiceSave(db *gorm.DB) gin.HandlerFunc {
	type ProductLine struct {
		Name     string `json:"name"`
		Quantity int    `json:"quantity"`
		IsNew    bool   `json:"is_new"`
	}
	type Request struct {
		ProviderName string        `json:"provider_name"`
		InvoiceTotal float64       `json:"invoice_total"`
		Products     []ProductLine `json:"products" binding:"required"`
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

		created, updated := 0, 0
		var lines []string
		for _, p := range req.Products {
			if p.IsNew {
				created++
			} else {
				updated++
			}
			tag := "nuevo"
			if !p.IsNew {
				tag = "restock"
			}
			lines = append(lines, fmt.Sprintf("• %s x%d (%s)", p.Name, p.Quantity, tag))
		}

		summary := strings.Join(lines, "\n")
		if req.ProviderName != "" {
			summary = fmt.Sprintf("Proveedor: %s\n%s", req.ProviderName, summary)
		}

		// Resolve user name
		userName := ""
		if userID != "" {
			var u struct{ Name string }
			if err := db.Table("users").Select("name").
				Where("id = ?", userID).Scan(&u).Error; err == nil {
				userName = u.Name
			}
		}

		log := models.InvoiceLog{
			ID:           uuid.NewString(),
			TenantID:     tenantID,
			BranchID:     middleware.UUIDPtr(branchID),
			UserID:       middleware.UUIDPtr(userID),
			UserName:     userName,
			ProviderName: req.ProviderName,
			ProductCount: len(req.Products),
			CreatedCount: created,
			UpdatedCount: updated,
			InvoiceTotal: req.InvoiceTotal,
			Summary:      summary,
		}

		if err := db.Create(&log).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al registrar factura"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": log})
	}
}

// ListInvoiceLogs returns the invoice scan history for the tenant.
// GET /api/v1/inventory/invoice-logs
func ListInvoiceLogs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		p := parsePagination(c)

		query := db.Model(&models.InvoiceLog{}).Where("tenant_id = ?", tenantID)
		if scope.BranchID != "" {
			query = query.Where("branch_id = ?", scope.BranchID)
		}

		var total int64
		query.Count(&total)

		var logs []models.InvoiceLog
		if err := query.Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&logs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener historial"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(logs, total, p))
	}
}
