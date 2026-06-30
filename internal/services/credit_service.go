package services

import (
	"errors"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCreditNotFound    = errors.New("crédito no encontrado")
	ErrPaymentExceeds    = errors.New("el abono excede el saldo pendiente")
	ErrCreditAlreadyPaid = errors.New("el crédito ya está pagado")
)

type CreditService struct {
	db *gorm.DB
}

func NewCreditService(db *gorm.DB) *CreditService {
	return &CreditService{db: db}
}

// RegisterPayment is kept for backward compatibility. New code should use
// RegisterPaymentWithActor so we record who registered the payment and at
// which branch — important for multi-workspace traceability.
func (s *CreditService) RegisterPayment(tenantID, creditID string, amount int64, method, note string) (*models.CreditPayment, error) {
	return s.RegisterPaymentWithActor(tenantID, creditID, "", "", amount, method, note, "")
}

// RegisterPaymentWithActor registers an abono. receiptImageURL is the
// Supabase Storage URL of the cashier's photo of the digital-payment
// confirmation; pass "" for cash abonos that don't carry one (the
// Mandatory Image Receipts epic enforces it on the frontend, not here).
func (s *CreditService) RegisterPaymentWithActor(tenantID, creditID, userID, branchID string, amount int64, method, note, receiptImageURL string) (*models.CreditPayment, error) {
	if method == "" {
		method = "cash"
	}

	var payment models.CreditPayment
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Row lock: re-read the account FOR UPDATE inside the transaction
		// so two concurrent abonos on the same credit (e.g. an offline-sync
		// replay racing a live POS payment) serialize instead of both
		// reading the same stale paid_amount and producing a lost update.
		var credit models.CreditAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ?", creditID, tenantID).
			First(&credit).Error; err != nil {
			return ErrCreditNotFound
		}

		if credit.Status == "paid" {
			return ErrCreditAlreadyPaid
		}

		remaining := credit.TotalAmount - credit.PaidAmount
		if amount > remaining {
			return ErrPaymentExceeds
		}

		payment = models.CreditPayment{
			CreditAccountID: creditID,
			CreatedBy:       middleware.UUIDPtr(userID),
			BranchID:        middleware.UUIDPtr(branchID),
			Amount:          amount,
			PaymentMethod:   method,
			Note:            note,
			ReceiptImageURL: receiptImageURL,
		}
		if err := tx.Create(&payment).Error; err != nil {
			return err
		}

		newPaid := credit.PaidAmount + amount
		newStatus := "partial"
		updates := map[string]any{
			"paid_amount": newPaid,
			"status":      newStatus,
		}
		if newPaid >= credit.TotalAmount {
			// Stamp closed_at when the account hits zero balance via
			// payments. Drives "Pagados" tab ordering on the cuaderno
			// screen — never reset, never overwritten.
			now := time.Now()
			updates["status"] = "paid"
			updates["closed_at"] = now
		}

		return tx.Model(&credit).Updates(updates).Error
	})

	if err != nil {
		return nil, err
	}

	return &payment, nil
}
