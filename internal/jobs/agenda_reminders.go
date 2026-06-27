// Spec: specs/084-peluqueria-salon/spec.md (backlog #1 — aviso diario al salón)
package jobs

import (
	"context"
	"fmt"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services/push"

	"gorm.io/gorm"
)

var bogotaLoc = time.FixedZone("America/Bogota", -5*60*60)

// AgendaReminderResult resume cuántos salones recibieron el aviso.
type AgendaReminderResult struct {
	Notified int `json:"notified"`
}

// RunAgendaReminders avisa a cada salón los turnos que tiene HOY (pendientes o
// confirmados). Pensado para correr cada mañana (cron). Idempotente por día vía
// DedupKey del dispatcher. `now` se pasa para testear.
func RunAgendaReminders(db *gorm.DB, now time.Time, dispatcher *push.Dispatcher) (AgendaReminderResult, error) {
	day := now.In(bogotaLoc)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, bogotaLoc)
	end := start.Add(24 * time.Hour)
	dateStr := start.Format("2006-01-02")

	// Conteo de turnos de hoy por tenant.
	type row struct {
		TenantID string
		N        int
	}
	var rows []row
	if err := db.Model(&models.Appointment{}).
		Select("tenant_id, COUNT(*) as n").
		Where("status IN ? AND starts_at >= ? AND starts_at < ?",
			[]string{models.AppointmentPending, models.AppointmentConfirmed}, start, end).
		Group("tenant_id").Scan(&rows).Error; err != nil {
		return AgendaReminderResult{}, err
	}

	res := AgendaReminderResult{}
	for _, r := range rows {
		if r.N == 0 {
			continue
		}
		title := "Turnos de hoy"
		body := fmt.Sprintf("Tiene %d turno%s agendado%s para hoy.",
			r.N, plural(r.N), plural(r.N))
		dedup := "agenda-reminder:" + r.TenantID + ":" + dateStr

		if dispatcher == nil {
			if err := db.Create(&models.Notification{
				TenantID: r.TenantID,
				Type:     "agenda_reminder",
				Title:    title,
				Body:     body,
			}).Error; err != nil {
				return res, err
			}
		} else if _, err := dispatcher.DispatchEvent(context.Background(), db, push.Event{
			TenantID: r.TenantID,
			Type:     "agenda_reminder",
			Title:    title,
			Body:     body,
			DeepLink: "/agenda",
			DedupKey: dedup,
		}); err != nil {
			return res, err
		}
		res.Notified++
	}
	return res, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
