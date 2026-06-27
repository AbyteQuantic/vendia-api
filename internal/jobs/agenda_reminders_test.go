// Spec: specs/084-peluqueria-salon/spec.md (backlog #1)
package jobs

import (
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRunAgendaReminders_NotifiesPerTenant(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Appointment{}))
	// gen_random_uuid() default rompe AutoMigrate de Notification en sqlite.
	require.NoError(t, db.Exec(`CREATE TABLE notifications (
		id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
		tenant_id TEXT NOT NULL, title TEXT DEFAULT '', body TEXT DEFAULT '',
		type TEXT DEFAULT 'info', is_read INTEGER DEFAULT 0, deep_link TEXT,
		pushed_at DATETIME, dedup_key TEXT)`).Error)

	loc := time.FixedZone("America/Bogota", -5*60*60)
	now := time.Now().In(loc)
	noon := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, loc)
	tenantID := uuid.NewString()

	// 2 turnos HOY (pendiente + confirmada) + 1 cancelado (no cuenta).
	for _, st := range []string{models.AppointmentPending, models.AppointmentConfirmed, models.AppointmentCancelled} {
		require.NoError(t, db.Create(&models.Appointment{
			BaseModel: models.BaseModel{ID: uuid.NewString()}, TenantID: tenantID,
			ServiceName: "Corte", StartsAt: noon, EndsAt: noon.Add(30 * time.Minute), Status: st,
		}).Error)
	}

	res, err := RunAgendaReminders(db, now.UTC(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Notified)

	var notif models.Notification
	require.NoError(t, db.First(&notif).Error)
	assert.Equal(t, "agenda_reminder", notif.Type)
	assert.Contains(t, notif.Body, "2 turnos")
}
