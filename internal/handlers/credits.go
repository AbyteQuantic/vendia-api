package handlers

import (
	"fmt"
	"log"
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
		userID := middleware.GetUserID(c)
		branchID := middleware.GetBranchID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		credit := models.CreditAccount{
			TenantID:    tenantID,
			CreatedBy:   userID,
			BranchID:    branchID,
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

// CloseCredit marks a credit account as settled even when a residual
// balance remains — used when the tendero negotiates a discount or writes
// off a small leftover. The residual is recorded as a CreditPayment with
// method='write_off' so the books still balance and the timeline has an
// auditable entry.
func CloseCredit(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Reason string `json:"reason"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		branchID := middleware.GetBranchID(c)
		creditID := c.Param("id")

		var req Request
		_ = c.ShouldBindJSON(&req) // reason is optional; ignore binding errors

		var credit models.CreditAccount
		if err := db.Where("id = ? AND tenant_id = ?", creditID, tenantID).
			First(&credit).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "crédito no encontrado"})
			return
		}

		if credit.Status == "paid" {
			c.JSON(http.StatusConflict, gin.H{"error": "la cuenta ya está cerrada"})
			return
		}

		remaining := credit.TotalAmount - credit.PaidAmount
		note := req.Reason
		if note == "" {
			note = "Saldo condonado al cerrar la cuenta"
		}

		// CreditPayment has nullable UUID columns (created_by, branch_id);
		// Postgres rejects empty-string inserts on UUID cols. Legacy tokens
		// without user/branch claims would crash here — use pointers so
		// GORM emits SQL NULL.
		var userPtr, branchPtr *string
		if userID != "" {
			userPtr = &userID
		}
		if branchID != "" {
			branchPtr = &branchID
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			if remaining > 0 {
				writeOff := map[string]any{
					"credit_account_id": creditID,
					"amount":            remaining,
					"payment_method":    "write_off",
					"note":              note,
				}
				if userPtr != nil {
					writeOff["created_by"] = *userPtr
				}
				if branchPtr != nil {
					writeOff["branch_id"] = *branchPtr
				}
				if err := tx.Model(&models.CreditPayment{}).Create(writeOff).Error; err != nil {
					return err
				}
			}
			return tx.Model(&credit).Updates(map[string]any{
				"paid_amount": credit.TotalAmount,
				"status":      "paid",
			}).Error
		})

		if err != nil {
			// Surface the DB error so the caller can see what actually broke.
			log.Printf("[close-credit] credit_id=%s tenant_id=%s error: %v",
				creditID, tenantID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("error al cerrar la cuenta: %v", err),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"credit_id":     credit.ID,
				"status":        "paid",
				"written_off":   remaining,
				"total_amount":  credit.TotalAmount,
				"reason":        note,
			},
		})
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
		userID := middleware.GetUserID(c)
		branchID := middleware.GetBranchID(c)
		creditID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		svc := services.NewCreditService(db)
		payment, err := svc.RegisterPaymentWithActor(tenantID, creditID, userID, branchID, req.Amount, req.PaymentMethod, req.Note)
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
