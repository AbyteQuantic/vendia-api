package handlers

import (
	"fmt"
	"log"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
	"vendia-backend/internal/services/push"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListCredits(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)
		status := c.Query("status")
		groupBy := c.Query("group_by")

		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{
				"error":      "la sucursal no pertenece al negocio",
				"error_code": "branch_not_owned",
			})
			return
		}

		// /credits?group_by=customer collapses every open-shape ledger
		// account into one row per customer with rolled-up balances.
		// Powers the cuaderno's main list — one entry per debtor, not
		// one per fiado event.
		if groupBy == "customer" {
			listCreditsGroupedByCustomer(db, c, tenantID, scope)
			return
		}

		query := db.Model(&models.CreditAccount{}).Where("tenant_id = ?", tenantID)
		// Bucket semantics for the cuaderno tabs (PO mandate — tabs
		// must be mutually exclusive):
		//
		//   * "pending" — fiado_status ∈ {pending, link_sent,
		//     link_opened}. The link was sent / opened but the
		//     customer has NOT accepted the fiado yet, so the
		//     merchandise is *not* a live debt. Ignores the
		//     `status` column on purpose — it can be `'pending'`
		//     or even `'open'` while the handshake is in flight,
		//     and we never want one row to bleed into both tabs.
		//
		//   * "paid"    — `status = 'paid'`. Ledger settlements,
		//     ordered by `closed_at DESC` below.
		//
		// Anything else is treated as a literal column-level
		// `status = ?` filter to keep the legacy contract for
		// callers outside the cuaderno screen.
		if status == "pending" {
			query = query.Where(
				"fiado_status IN ?",
				[]string{FiadoPending, FiadoLinkSent, FiadoLinkOpened},
			)
		} else if status != "" {
			query = query.Where("status = ?", status)
		}
		query = ApplyBranchScope(query, scope)

		var total int64
		query.Count(&total)

		// Pagados (status='paid') sorts by when the account closed —
		// most recent settlement first — instead of creation date,
		// because what the cashier wants to see is "qué se pagó hoy?".
		// NULLS LAST keeps legacy paid rows (closed before this column
		// existed) at the bottom rather than scrambling the top.
		listQuery := query.Preload("Customer")
		if status == "paid" {
			listQuery = listQuery.Order("closed_at DESC NULLS LAST")
		} else {
			listQuery = listQuery.Order("created_at DESC")
		}

		var credits []models.CreditAccount
		if err := listQuery.
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&credits).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener créditos"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(credits, total, p))
	}
}

// listCreditsGroupedByCustomer rolls every accepted, unpaid ledger
// account into a single row per customer. The cuaderno's "Activos"
// tab uses this — without the rollup the list could show three
// "Viviana — $50.000" rows for the same debtor.
//
// PO mandate (Tab Bleeding fix): an account is only "live debt"
// when the customer formally accepted the fiado. We require
//
//	status        ∈ {open, partial}     -- balance still pending
//	fiado_status  =  accepted            -- customer agreed to owe it
//
// Pending handshakes (link_sent / link_opened / pending) belong in
// the "Pendientes" tab exclusively and must NOT show up here, even
// though their column-level status is technically `pending`.
//
// "Worst-case" priority among the surviving rows: open > partial.
func listCreditsGroupedByCustomer(db *gorm.DB, c *gin.Context, tenantID string, scope BranchScopeResolution) {
	// LatestAt is intentionally a string. Postgres returns a
	// time.Time for MAX(updated_at), but the SQLite test driver
	// surfaces it as text — keeping it as a string makes the
	// query portable and lets the client parse if it cares (the
	// cuaderno screen only shows "balance ↓" sort today; this
	// field is informational).
	type Row struct {
		CustomerID    string `json:"customer_id"`
		CustomerName  string `json:"customer_name"`
		CustomerPhone string `json:"customer_phone"`
		TotalAmount   int64  `json:"total_amount"`
		PaidAmount    int64  `json:"paid_amount"`
		Balance       int64  `json:"balance"`
		AccountsCount int64  `json:"accounts_count"`
		LatestAt      string `json:"latest_activity_at"`
		Status        string `json:"status"`
	}

	// MAX(CASE ...) instead of BOOL_OR keeps the query portable
	// across Postgres (production) and SQLite (unit tests). After
	// the fiado_status='accepted' guard the only surviving values
	// are 'open' and 'partial' — open wins by mandate.
	q := db.Table("credit_accounts AS ca").
		Select(`
			ca.customer_id,
			COALESCE(c.name, '') AS customer_name,
			COALESCE(c.phone, '') AS customer_phone,
			COALESCE(SUM(ca.total_amount), 0) AS total_amount,
			COALESCE(SUM(ca.paid_amount), 0) AS paid_amount,
			COALESCE(SUM(ca.total_amount - ca.paid_amount), 0) AS balance,
			COUNT(*) AS accounts_count,
			MAX(ca.updated_at) AS latest_at,
			CASE
			  WHEN MAX(CASE WHEN ca.status = 'open'    THEN 1 ELSE 0 END) = 1 THEN 'open'
			  ELSE 'partial'
			END AS status
		`).
		Joins("LEFT JOIN customers c ON c.id = ca.customer_id AND c.deleted_at IS NULL").
		Where("ca.tenant_id = ? AND ca.deleted_at IS NULL", tenantID).
		Where("ca.status IN ?", []string{"open", "partial"}).
		Where("ca.fiado_status = ?", FiadoAccepted).
		Group("ca.customer_id, c.name, c.phone").
		Order("balance DESC")

	q = ApplyBranchScope(q, scope)

	var rows []Row
	if err := q.Find(&rows).Error; err != nil {
		log.Printf("[list-credits] group_by=customer tenant=%s: %v", tenantID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo agrupar"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
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
			CreatedBy:   middleware.UUIDPtr(userID),
			BranchID:    middleware.UUIDPtr(branchID),
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

// CancelCredit voids a pending (not yet accepted) fiado and reverses
// every side effect: linked sales are soft-deleted, stock is returned
// to the products, and the credit account flips to status='cancelled'.
// Only callable while the customer hasn't signed — once accepted the
// flow shifts to abono / write-off / refund semantics.
func CancelCredit(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Reason string `json:"reason"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		creditID := c.Param("id")

		var req Request
		_ = c.ShouldBindJSON(&req)

		var credit models.CreditAccount
		if err := db.Preload("Customer").
			Where("id = ? AND tenant_id = ?", creditID, tenantID).
			First(&credit).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "crédito no encontrado"})
			return
		}

		// Only pending credits can be cancelled. An accepted account is
		// a line of credit — reversing it requires a refund or write-off
		// flow, which is a different UX contract.
		if credit.Status != "pending" {
			c.JSON(http.StatusConflict, gin.H{
				"error": "solo se pueden cancelar fiados pendientes (no aceptados por el cliente)",
			})
			return
		}

		// Collect every sale that was wired to this credit account,
		// including the original one (if CreditAccount.SaleID is set).
		var sales []models.Sale
		saleQuery := db.Preload("Items").Where("tenant_id = ?", tenantID)
		conds := "credit_account_id = ?"
		args := []any{credit.ID}
		if credit.SaleID != nil && *credit.SaleID != "" {
			conds = "credit_account_id = ? OR id = ?"
			args = []any{credit.ID, *credit.SaleID}
		}
		saleQuery.Where(conds, args...).Find(&sales)

		restoredItems := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, sale := range sales {
				for _, item := range sale.Items {
					// Container charges and service lines are virtual —
					// no inventory row to restore.
					if item.IsContainerCharge || item.IsService || item.ProductID == nil {
						continue
					}
					pid := *item.ProductID
					services.LogInventoryMovement(tx, services.MovementParams{
						TenantID:      tenantID,
						ProductID:     pid,
						ProductName:   item.Name,
						MovementType:  models.MovementSaleCancel,
						Quantity:      item.Quantity,
						ReferenceID:   &credit.ID,
						ReferenceType: "credit",
						UserID:        middleware.UUIDPtr(middleware.GetUserID(c)),
					})
					if err := tx.Model(&models.Product{}).
						Where("id = ? AND tenant_id = ?", pid, tenantID).
						UpdateColumn("stock", gorm.Expr("stock + ?", item.Quantity)).Error; err != nil {
						return fmt.Errorf("restore stock product=%s: %w", pid, err)
					}
					restoredItems++
				}
				// Soft-delete the sale so analytics and receipts don't
				// pick it up anymore. The row stays for audit via
				// deleted_at.
				if err := tx.Delete(&sale).Error; err != nil {
					return fmt.Errorf("soft-delete sale=%s: %w", sale.ID, err)
				}
			}
			return tx.Model(&credit).Updates(map[string]any{
				"status":       "cancelled",
				"fiado_status": "cancelled",
			}).Error
		})
		if err != nil {
			log.Printf("[cancel-credit] credit=%s tenant=%s: %v",
				creditID, tenantID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("error al cancelar fiado: %v", err),
			})
			return
		}

		// Inform the tendero in the notifications feed.
		go func(tenantID, customerName, reason string, amount int64) {
			body := fmt.Sprintf("Stock restaurado. Monto cancelado: $%d.", amount)
			if reason != "" {
				body = body + " Motivo: " + reason
			}
			notif := models.Notification{
				TenantID: tenantID,
				Title:    fmt.Sprintf("Fiado cancelado — %s", customerName),
				Body:     body,
				Type:     "fiado_cancelled",
			}
			_ = db.Create(&notif).Error
		}(credit.TenantID, credit.Customer.Name, req.Reason, credit.TotalAmount)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"credit_id":      credit.ID,
				"status":         "cancelled",
				"sales_voided":   len(sales),
				"items_restored": restoredItems,
			},
		})
	}
}

// CloseCredit marks a credit account as settled. By default it refuses
// to close an account that still has a positive balance — closing with
// debt is a write-off (discount / forgiven leftover) and the caller must
// opt in explicitly with {"force":true}. The residual is recorded as a
// CreditPayment with method='write_off' so the books stay balanced and
// the timeline has an auditable entry.
func CloseCredit(db *gorm.DB, dispatcher *push.Dispatcher) gin.HandlerFunc {
	type Request struct {
		Reason string `json:"reason"`
		Force  bool   `json:"force"`
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
		// Safety rule — refuse to close with debt unless the caller opts
		// in to a write-off via force=true. Protects against accidental
		// "Cerrar cuenta" taps that would erase a real balance.
		if remaining > 0 && !req.Force {
			c.JSON(http.StatusConflict, gin.H{
				"error":   "la cuenta aún tiene saldo pendiente",
				"balance": remaining,
				"hint":    "registre abonos hasta saldar, o use force=true para condonar el saldo",
			})
			return
		}
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

		now := time.Now()
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
			// Stamp closed_at so "Pagados" tab can order by it. The
			// status flip alone wouldn't tell us WHEN the customer
			// settled vs when they originally opened the fiado.
			return tx.Model(&credit).Updates(map[string]any{
				"paid_amount": credit.TotalAmount,
				"status":      "paid",
				"closed_at":   now,
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

		// Spec F038 — notificar cierre de cuenta al tendero.
		if dispatcher != nil {
			_, _ = dispatcher.DispatchEvent(c.Request.Context(), db, push.Event{
				TenantID: tenantID,
				Type:     "credit_close",
				Title:    "Cuenta cerrada",
				Body:     "Una cuenta de crédito quedó saldada",
				DeepLink: "/cuaderno/" + credit.ID,
				DedupKey: "credit-close:" + credit.ID,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"credit_id":    credit.ID,
				"status":       "paid",
				"written_off":  remaining,
				"total_amount": credit.TotalAmount,
				"reason":       note,
			},
		})
	}
}

func CreatePayment(db *gorm.DB, dispatcher *push.Dispatcher) gin.HandlerFunc {
	type Request struct {
		Amount        int64  `json:"amount" binding:"required,gt=0"`
		PaymentMethod string `json:"payment_method"`
		Note          string `json:"note"`
		// ReceiptImageURL — Supabase Storage URL of the cashier's photo of
		// the digital-payment confirmation. Optional at the API layer
		// (frontend enforces it for digital methods); cash abonos legitly
		// omit it. See models.CreditPayment for the storage contract.
		ReceiptImageURL *string `json:"receipt_image_url"`
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

		receiptURL := ""
		if req.ReceiptImageURL != nil {
			receiptURL = *req.ReceiptImageURL
			if !isAbsoluteHTTPURL(receiptURL) {
				log.Printf("[create-payment] tenant=%s credit=%s non-absolute receipt_image_url=%q",
					tenantID, creditID, receiptURL)
			}
		}

		svc := services.NewCreditService(db)
		payment, err := svc.RegisterPaymentWithActor(tenantID, creditID, userID, branchID, req.Amount, req.PaymentMethod, req.Note, receiptURL)
		if err != nil {
			if err == services.ErrCreditNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "crédito no encontrado"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Spec F038 — notificar abono al tendero (push + in-app).
		if dispatcher != nil {
			_, _ = dispatcher.DispatchEvent(c.Request.Context(), db, push.Event{
				TenantID: tenantID,
				Type:     "credit_payment",
				Title:    "Abono recibido",
				Body:     fmt.Sprintf("Se registró un abono por $%.0f", float64(req.Amount)),
				DeepLink: "/cuaderno/" + creditID,
				DedupKey: "credit-payment:" + payment.ID,
			})
		}

		c.JSON(http.StatusCreated, gin.H{"data": payment})
	}
}
