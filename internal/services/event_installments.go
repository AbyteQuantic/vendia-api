// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"errors"
	"fmt"

	"vendia-backend/internal/models"
)

// ErrTooManyInstallments is returned when the number of installments would
// force a cuota below the $50 minimum unit.
var ErrTooManyInstallments = errors.New("demasiadas cuotas para el monto del evento")

// ComputeInstallments splits a total (a multiple of $50 COP) into n cuotas
// such that each cuota is a multiple of $50 and the cuotas sum EXACTLY to the
// total (Art. VII — money is exact, no centavo lost). The remainder is spread
// one $50-unit at a time across the first cuotas, so amounts differ by at most
// $50.
func ComputeInstallments(total int64, n int) ([]int64, error) {
	if n <= 0 {
		return nil, errors.New("el número de cuotas debe ser mayor a cero")
	}
	if total <= 0 || total%50 != 0 {
		return nil, errors.New("el monto a diferir debe ser múltiplo de $50 y mayor a cero")
	}
	units := total / 50 // work in $50 units to guarantee exact multiples
	if int64(n) > units {
		return nil, ErrTooManyInstallments
	}
	base := units / int64(n)
	rem := units % int64(n)

	schedule := make([]int64, n)
	for i := 0; i < n; i++ {
		u := base
		if int64(i) < rem {
			u++
		}
		schedule[i] = u * 50
	}
	return schedule, nil
}

// SetupInstallments creates the event-scoped fiado account for a registration
// and returns it with the computed cuota schedule. Per decision R-02 the
// account is SEPARATE from any store fiado account — it is linked only from
// the registration, never merged with the customer's store credit.
func (s *EventRegistrationService) SetupInstallments(tenantID, registrationID string) (*models.CreditAccount, []int64, error) {
	var reg models.EventRegistration
	if err := s.db.Where("id = ? AND tenant_id = ?", registrationID, tenantID).First(&reg).Error; err != nil {
		return nil, nil, ErrRegistrationNotFound
	}

	var ev models.Event
	if err := s.db.Where("id = ? AND tenant_id = ?", reg.EventID, tenantID).First(&ev).Error; err != nil {
		return nil, nil, ErrEventNotFound
	}
	if !ev.InstallmentsEnabled || ev.InstallmentsCount <= 0 {
		return nil, nil, errors.New("este evento no admite pago en cuotas")
	}

	schedule, err := ComputeInstallments(ev.Price, ev.InstallmentsCount)
	if err != nil {
		return nil, nil, err
	}

	account := &models.CreditAccount{
		TenantID:    tenantID,
		CustomerID:  reg.CustomerID,
		TotalAmount: ev.Price,
		Status:      "open",
		Description: fmt.Sprintf("Cuotas evento: %s", ev.Title),
	}
	if err := s.db.Create(account).Error; err != nil {
		return nil, nil, fmt.Errorf("crear cuenta de cuotas del evento: %w", err)
	}

	if err := s.db.Model(&reg).Update("credit_account_id", account.ID).Error; err != nil {
		return nil, nil, fmt.Errorf("vincular cuenta de cuotas: %w", err)
	}
	return account, schedule, nil
}
