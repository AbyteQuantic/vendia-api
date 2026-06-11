// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"errors"
	"fmt"
	"time"

	"vendia-backend/internal/models"
)

// installmentSpacing is the gap between consecutive cuotas in the manual plan.
const installmentSpacing = 30 * 24 * time.Hour

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

// InstallmentCuota is one dated cuota in the derived (on-the-fly) plan shown to
// the attendee and the organizer.
type InstallmentCuota struct {
	Number  int       `json:"number"`
	Amount  int64     `json:"amount"`
	DueDate time.Time `json:"due_date"`
	Status  string    `json:"status"` // pagada | pendiente | vencida
}

// InstallmentPlan is the per-attendee cuota schedule derived from the
// registration date, the event start and what's been paid. It is computed on
// the fly (not persisted) so it always reflects the current event start and
// payments — no migration, no staleness.
type InstallmentPlan struct {
	Count          int                `json:"count"`
	PaidCount      int                `json:"paid_count"`
	RemainingCount int                `json:"remaining_count"`
	OverdueCount   int                `json:"overdue_count"`
	OverdueAmount  int64              `json:"overdue_amount"`
	NextDueNumber  int                `json:"next_due_number,omitempty"`
	NextDueDate    *time.Time         `json:"next_due_date,omitempty"`
	NextDueAmount  int64              `json:"next_due_amount,omitempty"`
	FinalDueDate   *time.Time         `json:"final_due_date,omitempty"`
	Cuotas         []InstallmentCuota `json:"cuotas"`
}

// BuildInstallmentPlan derives the cuota schedule for one registration.
// Decision (confirmed with the organizer): the FIRST cuota is due at the
// registration date and the rest are spread evenly up to the event start — the
// last cuota falls on the start, since the carné only activates when the whole
// price is paid. Amounts come from ComputeInstallments (exact $50 split). Each
// cuota is marked pagada / pendiente / vencida from amountPaid (cumulative) and
// `now`. Returns nil when there's no usable plan (cuotas < 2, price <= 0, or the
// amounts can't be split), so callers can simply omit it.
func BuildInstallmentPlan(
	registeredAt time.Time,
	startAt *time.Time,
	count int,
	price, amountPaid int64,
	now time.Time,
) *InstallmentPlan {
	if count < 2 || price <= 0 {
		return nil
	}
	amounts, err := ComputeInstallments(price, count)
	if err != nil {
		return nil
	}

	// Due dates: cuota 1 at registration, cuota N at the event start, the rest
	// evenly in between. If the start is missing or not after registration (very
	// late sign-up), the window collapses and every cuota is due now.
	end := registeredAt
	if startAt != nil && startAt.After(registeredAt) {
		end = *startAt
	}
	window := end.Sub(registeredAt)
	dues := make([]time.Time, count)
	for i := 0; i < count; i++ {
		frac := float64(i) / float64(count-1)
		dues[i] = registeredAt.Add(time.Duration(float64(window) * frac))
	}

	plan := &InstallmentPlan{Count: count, Cuotas: make([]InstallmentCuota, count)}
	final := dues[count-1]
	plan.FinalDueDate = &final
	var cum int64
	for i := 0; i < count; i++ {
		paidBefore := cum
		cum += amounts[i]
		status := InstallmentStatusPending
		if amountPaid >= cum {
			status = InstallmentStatusPaid
			plan.PaidCount++
		} else {
			plan.RemainingCount++
			// Unpaid portion of THIS cuota (handles a partial abono landing mid-cuota).
			unpaid := cum - max(amountPaid, paidBefore)
			if dues[i].Before(now) {
				status = InstallmentStatusOverdue
				plan.OverdueCount++
				plan.OverdueAmount += unpaid
			}
			if plan.NextDueNumber == 0 {
				plan.NextDueNumber = i + 1
				d := dues[i]
				plan.NextDueDate = &d
				plan.NextDueAmount = unpaid
			}
		}
		plan.Cuotas[i] = InstallmentCuota{
			Number:  i + 1,
			Amount:  amounts[i],
			DueDate: dues[i],
			Status:  status,
		}
	}
	return plan
}

// InstallmentStatus* mirror the persisted model's statuses so the derived plan
// speaks the same vocabulary (Spanish, surfaced to users).
const (
	InstallmentStatusPending = models.InstallmentStatusPending
	InstallmentStatusPaid    = models.InstallmentStatusPaid
	InstallmentStatusOverdue = models.InstallmentStatusOverdue
)

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

	// D3: persist the dated schedule (one row per cuota) so reminders can be
	// precise about "cuota próxima/vencida". First cuota due ~30 days out,
	// then every 30 days.
	base := time.Now().UTC()
	for i, amount := range schedule {
		due := base.Add(time.Duration(i+1) * installmentSpacing)
		row := &models.EventInstallment{
			TenantID:        tenantID,
			RegistrationID:  reg.ID,
			CreditAccountID: &account.ID,
			Number:          i + 1,
			Amount:          amount,
			DueDate:         due,
			Status:          models.InstallmentStatusPending,
		}
		if err := s.db.Create(row).Error; err != nil {
			return nil, nil, fmt.Errorf("crear cuota %d: %w", i+1, err)
		}
	}
	return account, schedule, nil
}
