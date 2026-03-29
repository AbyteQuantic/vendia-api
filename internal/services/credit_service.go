package services

import (
	"errors"
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

var (
	ErrCreditNotFound  = errors.New("crédito no encontrado")
	ErrPaymentExceeds  = errors.New("el abono excede el saldo pendiente")
	ErrCreditAlreadyPaid = errors.New("el crédito ya está pagado")
)

type CreditService struct {
	db *gorm.DB
}

func NewCreditService(db *gorm.DB) *CreditService {
	return &CreditService{db: db}
}

func (s *CreditService) RegisterPayment(tenantID, creditID string, amount int64, method, note string) (*models.CreditPayment, error) {
	var credit models.CreditAccount
	if err := s.db.Where("id = ? AND tenant_id = ?", creditID, tenantID).
		First(&credit).Error; err != nil {
		return nil, ErrCreditNotFound
	}

	if credit.Status == "paid" {
		return nil, ErrCreditAlreadyPaid
	}

	remaining := credit.TotalAmount - credit.PaidAmount
	if amount > remaining {
		return nil, ErrPaymentExceeds
	}

	if method == "" {
		method = "cash"
	}

	var payment models.CreditPayment
	err := s.db.Transaction(func(tx *gorm.DB) error {
		payment = models.CreditPayment{
			CreditAccountID: creditID,
			Amount:          amount,
			PaymentMethod:   method,
			Note:            note,
		}
		if err := tx.Create(&payment).Error; err != nil {
			return err
		}

		newPaid := credit.PaidAmount + amount
		newStatus := "partial"
		if newPaid >= credit.TotalAmount {
			newStatus = "paid"
		}

		return tx.Model(&credit).Updates(map[string]any{
			"paid_amount": newPaid,
			"status":      newStatus,
		}).Error
	})

	if err != nil {
		return nil, err
	}

	return &payment, nil
}
