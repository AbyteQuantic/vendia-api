package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListCredits(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)
		status := c.Query("status")

		query := db.Model(&models.CreditAccount{}).Where("tenant_id = ?", tenantID)
		if status != "" {
			query = query.Where("status = ?", status)
		}

		var total int64
		query.Count(&total)

		var credits []models.CreditAccount
		if err := query.
			Preload("Customer").
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&credits).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener créditos"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(credits, total, p))
	}
}

func CreateCredit(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		CustomerID  string `json:"customer_id" binding:"required"`
		SaleID      string `json:"sale_id"     binding:"required"`
		TotalAmount int64  `json:"total_amount" binding:"required,gt=0"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		credit := models.CreditAccount{
			TenantID:    tenantID,
			CustomerID:  req.CustomerID,
			SaleID:      &req.SaleID,
			TotalAmount: req.TotalAmount,
			Status:      "open",
		}

		if err := db.Create(&credit).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear crédito"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": credit})
	}
}

func GetCredit(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		creditID := c.Param("id")

		var credit models.CreditAccount
		if err := db.Preload("Customer").Preload("Sale").Preload("Payments").
			Where("id = ? AND tenant_id = ?", creditID, tenantID).
			First(&credit).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "crédito no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": credit})
	}
}

func CreatePayment(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Amount        int64  `json:"amount" binding:"required,gt=0"`
		PaymentMethod string `json:"payment_method"`
		Note          string `json:"note"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		creditID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		svc := services.NewCreditService(db)
		payment, err := svc.RegisterPayment(tenantID, creditID, req.Amount, req.PaymentMethod, req.Note)
		if err != nil {
			if err == services.ErrCreditNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "crédito no encontrado"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": payment})
	}
}
