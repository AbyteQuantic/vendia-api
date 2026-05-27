// Spec: specs/033-difusion-promociones/spec.md
package jobs_test

import (
	"testing"
	"time"

	"vendia-backend/internal/jobs"
	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupPushDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.BroadcastPromotion{},
		&models.BroadcastPromotionDelivery{},
	))
	// Notifications uses a Postgres-only gen_random_uuid() default; stand
	// up an equivalent table by hand (id filled by BeforeCreate). The
	// `data` column was added in F38 for deep-link payloads.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			tenant_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT DEFAULT '',
			type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0,
			data TEXT DEFAULT '{}'
		)
	`).Error)
	return db
}

func mkPromo(t *testing.T, db *gorm.DB, id string, scheduledFor *time.Time, pushSent bool) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&models.BroadcastPromotion{
		BaseModel:        models.BaseModel{ID: id},
		TenantID:         "tenant-1",
		Title:            "Promo " + id,
		ValidFrom:        now,
		ValidUntil:       now.AddDate(0, 0, 7),
		PublicToken:      id + "-token",
		ScheduledFor:     scheduledFor,
		SchedulePushSent: pushSent,
	}).Error)
}

func TestRunPromotionsPush_NotifiesDuePromotions(t *testing.T) {
	db := setupPushDB(t)
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	future := now.Add(2 * time.Hour)

	mkPromo(t, db, "due-1", &past, false)      // due, not yet pushed → notify
	mkPromo(t, db, "future-1", &future, false) // scheduled later → skip
	mkPromo(t, db, "already-1", &past, true)   // due but already pushed → skip
	mkPromo(t, db, "instant-1", nil, false)    // no schedule (enviar ahora) → skip

	res, err := jobs.RunPromotionsPush(db, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Notified, "solo la promo vencida y no avisada")

	// schedule_push_sent must be flipped on the notified promo.
	var due models.BroadcastPromotion
	require.NoError(t, db.First(&due, "id = ?", "due-1").Error)
	assert.True(t, due.SchedulePushSent, "la promo notificada queda marcada")

	// A notification row was written for the tenant.
	var notifCount int64
	db.Table("notifications").
		Where("tenant_id = ? AND type = ?", "tenant-1", "promotion_schedule").
		Count(&notifCount)
	assert.EqualValues(t, 1, notifCount, "se crea una notificación")
}

func TestRunPromotionsPush_IsIdempotent(t *testing.T) {
	db := setupPushDB(t)
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	mkPromo(t, db, "due-1", &past, false)

	first, err := jobs.RunPromotionsPush(db, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, first.Notified)

	// Second run on the same data must notify nobody — schedule_push_sent
	// already flipped, so the owner is never double-notified.
	second, err := jobs.RunPromotionsPush(db, now)
	require.NoError(t, err)
	assert.EqualValues(t, 0, second.Notified, "segundo run no re-notifica")
}

func TestRunPromotionsPush_NoDuePromotions(t *testing.T) {
	db := setupPushDB(t)
	now := time.Now().UTC()

	res, err := jobs.RunPromotionsPush(db, now)
	require.NoError(t, err)
	assert.EqualValues(t, 0, res.Notified)
}
