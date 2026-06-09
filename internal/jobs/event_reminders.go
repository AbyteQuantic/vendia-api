// Spec: specs/042-modulo-eventos/spec.md
package jobs

import (
	"context"
	"fmt"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services/email"
	"vendia-backend/internal/services/push"

	"gorm.io/gorm"
)

// EventReminderWindow is how far ahead an event must start to trigger a
// reminder run (the cron runs daily).
const EventReminderWindow = 24 * time.Hour

// QuotaReminderWindow is how soon a pending cuota's due date must be (or how
// overdue) to trigger a reminder (D3 — uses the persisted schedule).
const QuotaReminderWindow = 3 * 24 * time.Hour

// EventReminderResult summarizes a reminder run for the cron response.
type EventReminderResult struct {
	EventRemindersSent int `json:"event_reminders_sent"`
	QuotaRemindersSent int `json:"quota_reminders_sent"`
	OrganizerPushes    int `json:"organizer_pushes"`
}

// RunEventReminders emails attendees about upcoming events and pending
// installments, and pushes each organizer a summary of their upcoming event
// (Spec F042 FR-20). Attendee push is intentionally NOT used: the F038 push
// infra targets tenant staff devices, not external customers — attendees are
// reached by email. emailSvc and dispatcher are nil-tolerant so a partial
// deploy still runs.
func RunEventReminders(db *gorm.DB, now time.Time, dispatcher *push.Dispatcher, emailSvc *email.Service) (EventReminderResult, error) {
	var res EventReminderResult
	ctx := context.Background()

	// ── Upcoming-event reminders ──────────────────────────────────────
	var events []models.Event
	if err := db.Where("status = ? AND start_at IS NOT NULL AND start_at > ? AND start_at <= ?",
		models.EventStatusPublicado, now, now.Add(EventReminderWindow)).Find(&events).Error; err != nil {
		return res, err
	}

	for _, ev := range events {
		regs, err := confirmedRegistrationsWithCustomer(db, ev.TenantID, ev.ID)
		if err != nil {
			return res, err
		}
		whenStr := ev.StartAt.Format("02/01 15:04")
		for _, rc := range regs {
			if emailSvc != nil && rc.Email != "" {
				if err := emailSvc.SendEventReminder(ctx, email.EventReminder{
					To: rc.Email, Name: rc.Name, EventTitle: ev.Title, WhenStr: whenStr,
				}); err == nil {
					res.EventRemindersSent++
				}
			}
		}
		// Summary push to the organizer's own devices.
		if dispatcher != nil && len(regs) > 0 {
			_, _ = dispatcher.DispatchEvent(ctx, db, push.Event{
				TenantID: ev.TenantID,
				Type:     "event_reminder",
				Title:    "Evento próximo",
				Body:     fmt.Sprintf("\"%s\" es %s · %d inscritos", ev.Title, whenStr, len(regs)),
				DedupKey: "event-reminder-" + ev.ID,
			})
			res.OrganizerPushes++
		}
	}

	// ── Pending-installment reminders ─────────────────────────────────
	res.QuotaRemindersSent = sendQuotaReminders(ctx, db, emailSvc, now)
	return res, nil
}

// regCustomer is a registration joined with its attendee contact info.
type regCustomer struct {
	Name  string
	Email string
	Phone string
}

func confirmedRegistrationsWithCustomer(db *gorm.DB, tenantID, eventID string) ([]regCustomer, error) {
	var out []regCustomer
	err := db.Table("event_registrations AS r").
		Select("c.name AS name, c.email AS email, c.phone AS phone").
		Joins("JOIN customers c ON c.id = r.customer_id").
		Where("r.tenant_id = ? AND r.event_id = ? AND r.payment_status = ? AND r.deleted_at IS NULL",
			tenantID, eventID, models.RegistrationPaymentConfirmed).
		Scan(&out).Error
	return out, err
}

// sendQuotaReminders emails attendees about pending cuotas whose due date is
// near or past (D3 — uses the persisted EventInstallment schedule for precise
// "próxima/vencida" reminders).
func sendQuotaReminders(ctx context.Context, db *gorm.DB, emailSvc *email.Service, now time.Time) int {
	if emailSvc == nil {
		return 0
	}
	type row struct {
		Name    string
		Email   string
		Title   string
		Amount  int64
		DueDate time.Time
	}
	var rows []row
	err := db.Table("event_installments AS i").
		Select("c.name AS name, c.email AS email, e.title AS title, i.amount AS amount, i.due_date AS due_date").
		Joins("JOIN event_registrations r ON r.id = i.registration_id").
		Joins("JOIN customers c ON c.id = r.customer_id").
		Joins("JOIN events e ON e.id = r.event_id").
		Where("i.status = ? AND i.deleted_at IS NULL AND i.due_date <= ?",
			models.InstallmentStatusPending, now.Add(QuotaReminderWindow)).
		Scan(&rows).Error
	if err != nil {
		return 0
	}
	sent := 0
	for _, r := range rows {
		if r.Email == "" {
			continue
		}
		if err := emailSvc.SendQuotaReminder(ctx, email.QuotaReminder{
			To: r.Email, Name: r.Name, EventTitle: r.Title,
			AmountStr: fmt.Sprintf("$%d", r.Amount), DueDateStr: r.DueDate.Format("02/01/2006"),
		}); err == nil {
			sent++
		}
	}
	return sent
}
