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

// ProofRetentionAfterEnd: los comprobantes de pago se conservan para revisión
// como máximo 15 días después de que el evento termina; luego se purga la
// referencia (privacidad + limpieza). Pedido del dueño.
const ProofRetentionAfterEnd = 15 * 24 * time.Hour

// publicSiteURL is the public catalog host. The reminder deep link carries the
// attendee's token (?reg=) so opening it leaves them "logged in" to see their
// event component (countdown, ubicación, estado de pago) and carné.
const publicSiteURL = "https://tienda.vendia.store"

// attendeeLink builds the catalog deep link for an attendee. Empty if the
// tenant has no public slug yet.
func attendeeLink(slug, token string) string {
	if slug == "" || token == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/menu?reg=%s", publicSiteURL, slug, token)
}

// formatMoneyDots renders an int amount as "$1.550.000" (es-CO thousands).
func formatMoneyDots(amount int64) string {
	if amount < 0 {
		amount = 0
	}
	s := fmt.Sprintf("%d", amount)
	var b []byte
	for i, r := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, '.')
		}
		b = append(b, r)
	}
	return "$" + string(b)
}

// EventReminderResult summarizes a reminder run for the cron response.
type EventReminderResult struct {
	EventRemindersSent int `json:"event_reminders_sent"`
	QuotaRemindersSent int `json:"quota_reminders_sent"`
	OrganizerPushes    int `json:"organizer_pushes"`
	ProofsPurged       int `json:"proofs_purged"`
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
		slug := tenantSlug(db, ev.TenantID)
		regs, err := confirmedRegistrationsWithCustomer(db, ev.TenantID, ev.ID)
		if err != nil {
			return res, err
		}
		whenStr := ev.StartAt.Format("02/01 15:04")
		for _, rc := range regs {
			if emailSvc != nil && rc.Email != "" {
				if err := emailSvc.SendEventReminder(ctx, email.EventReminder{
					To: rc.Email, Name: rc.Name, EventTitle: ev.Title, WhenStr: whenStr,
					Link: attendeeLink(slug, rc.PublicToken),
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

	// ── Limpieza: comprobantes de pago de eventos terminados hace +15 días ─
	res.ProofsPurged = purgeExpiredProofs(db, now)
	return res, nil
}

// purgeExpiredProofs borra la referencia al comprobante (proof_url) de los
// pagos cuyos eventos terminaron hace más de ProofRetentionAfterEnd. Usa el
// fin del evento, o el inicio si no hay fin. Solo limpia la referencia en BD
// (deja de mostrarse en la bandeja del organizador); el objeto en R2 se
// recolecta aparte. Devuelve cuántas referencias purgó.
func purgeExpiredProofs(db *gorm.DB, now time.Time) int {
	cutoff := now.Add(-ProofRetentionAfterEnd)
	var eventIDs []string
	db.Model(&models.Event{}).
		Where("COALESCE(end_at, start_at) IS NOT NULL AND COALESCE(end_at, start_at) < ?", cutoff).
		Pluck("id", &eventIDs)
	if len(eventIDs) == 0 {
		return 0
	}
	r := db.Model(&models.EventPayment{}).
		Where("event_id IN ? AND proof_url <> ''", eventIDs).
		Update("proof_url", "")
	if r.Error != nil {
		return 0
	}
	return int(r.RowsAffected)
}

// regCustomer is a registration joined with its attendee contact info.
type regCustomer struct {
	Name        string
	Email       string
	Phone       string
	PublicToken string
}

func confirmedRegistrationsWithCustomer(db *gorm.DB, tenantID, eventID string) ([]regCustomer, error) {
	var out []regCustomer
	err := db.Table("event_registrations AS r").
		Select("c.name AS name, c.email AS email, c.phone AS phone, r.public_token AS public_token").
		Joins("JOIN customers c ON c.id = r.customer_id").
		Where("r.tenant_id = ? AND r.event_id = ? AND r.payment_status = ? AND r.deleted_at IS NULL",
			tenantID, eventID, models.RegistrationPaymentConfirmed).
		Scan(&out).Error
	return out, err
}

// tenantSlug returns a tenant's public store slug (empty if none).
func tenantSlug(db *gorm.DB, tenantID string) string {
	var t models.Tenant
	if err := db.Select("store_slug").Where("id = ?", tenantID).First(&t).Error; err != nil {
		return ""
	}
	if t.StoreSlug == nil {
		return ""
	}
	return *t.StoreSlug
}

// sendQuotaReminders emails attendees who still owe a balance on a published,
// upcoming (or undated) paid event, with a deep link to pay. Uses the running
// AmountPaid balance (the abonos/cuotas flow), not the legacy installment rows.
func sendQuotaReminders(ctx context.Context, db *gorm.DB, emailSvc *email.Service, now time.Time) int {
	if emailSvc == nil {
		return 0
	}
	type row struct {
		Name        string
		Email       string
		Title       string
		Slug        *string
		PublicToken string
		Balance     int64
	}
	var rows []row
	err := db.Table("event_registrations AS r").
		Select("c.name AS name, c.email AS email, e.title AS title, t.store_slug AS slug, r.public_token AS public_token, (e.price - r.amount_paid) AS balance").
		Joins("JOIN customers c ON c.id = r.customer_id").
		Joins("JOIN events e ON e.id = r.event_id").
		Joins("JOIN tenants t ON t.id = r.tenant_id").
		Where("r.payment_status = ? AND r.deleted_at IS NULL AND e.status = ? AND e.price > 0 AND (e.price - r.amount_paid) > 0 AND (e.start_at IS NULL OR e.start_at > ?)",
			models.RegistrationPaymentPending, models.EventStatusPublicado, now).
		Scan(&rows).Error
	if err != nil {
		return 0
	}
	sent := 0
	for _, r := range rows {
		if r.Email == "" {
			continue
		}
		slug := ""
		if r.Slug != nil {
			slug = *r.Slug
		}
		if err := emailSvc.SendQuotaReminder(ctx, email.QuotaReminder{
			To: r.Email, Name: r.Name, EventTitle: r.Title,
			AmountStr: formatMoneyDots(r.Balance),
			Link:      attendeeLink(slug, r.PublicToken),
		}); err == nil {
			sent++
		}
	}
	return sent
}
