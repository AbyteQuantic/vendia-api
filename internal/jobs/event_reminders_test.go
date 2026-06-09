// Spec: specs/042-modulo-eventos/spec.md
package jobs_test

import (
	"testing"
	"time"

	"vendia-backend/internal/jobs"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services/email"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupReminderDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.Customer{}, &models.CreditAccount{},
	))
	return db
}

func TestRunEventReminders_EmailsUpcomingAttendees(t *testing.T) {
	db := setupReminderDB(t)
	now := time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC)
	start := now.Add(12 * time.Hour) // within the 24h window

	require.NoError(t, db.Create(&models.Event{
		BaseModel: models.BaseModel{ID: "ev1"}, TenantID: "t1", Title: "Curso",
		Modality: models.EventModalityVirtual, Status: models.EventStatusPublicado, StartAt: &start,
	}).Error)
	require.NoError(t, db.Create(&models.Customer{
		BaseModel: models.BaseModel{ID: "c1"}, TenantID: "t1", Name: "Ana", Email: "ana@example.com",
	}).Error)
	require.NoError(t, db.Create(&models.EventRegistration{
		BaseModel: models.BaseModel{ID: "r1"}, TenantID: "t1", EventID: "ev1", CustomerID: "c1",
		QRToken: "q1", PublicToken: "p1", PaymentStatus: models.RegistrationPaymentConfirmed,
	}).Error)

	fake := &email.FakeSender{}
	svc := email.NewServiceWithSender(fake, "eventos@vendia.store")

	res, err := jobs.RunEventReminders(db, now, nil, svc)
	require.NoError(t, err)
	assert.Equal(t, 1, res.EventRemindersSent)
	require.Len(t, fake.Sent, 1)
	assert.Equal(t, "ana@example.com", fake.Sent[0].To)
}

func TestRunEventReminders_SkipsFarAndUnconfirmed(t *testing.T) {
	db := setupReminderDB(t)
	now := time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC)
	far := now.Add(72 * time.Hour) // outside window

	require.NoError(t, db.Create(&models.Event{
		BaseModel: models.BaseModel{ID: "ev2"}, TenantID: "t1", Title: "Lejano",
		Modality: models.EventModalityVirtual, Status: models.EventStatusPublicado, StartAt: &far,
	}).Error)
	require.NoError(t, db.Create(&models.Customer{
		BaseModel: models.BaseModel{ID: "c2"}, TenantID: "t1", Name: "Beto", Email: "beto@example.com",
	}).Error)
	require.NoError(t, db.Create(&models.EventRegistration{
		BaseModel: models.BaseModel{ID: "r2"}, TenantID: "t1", EventID: "ev2", CustomerID: "c2",
		QRToken: "q2", PublicToken: "p2", PaymentStatus: models.RegistrationPaymentPending,
	}).Error)

	fake := &email.FakeSender{}
	svc := email.NewServiceWithSender(fake, "eventos@vendia.store")

	res, err := jobs.RunEventReminders(db, now, nil, svc)
	require.NoError(t, err)
	assert.Equal(t, 0, res.EventRemindersSent)
	assert.Empty(t, fake.Sent)
}
