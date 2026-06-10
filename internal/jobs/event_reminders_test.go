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
		&models.Event{}, &models.EventRegistration{}, &models.Customer{},
		&models.CreditAccount{}, &models.EventInstallment{}, &models.Tenant{},
	))
	return db
}

func seedTenant(t *testing.T, db *gorm.DB, id, slug string) {
	t.Helper()
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: id}, OwnerName: "Org", Phone: "3000000000",
		StoreSlug: &slug,
	}).Error)
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

func TestRunEventReminders_QuotaBalancePending(t *testing.T) {
	db := setupReminderDB(t)
	seedTenant(t, db, "t1", "dulce-ana")
	now := time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC)

	// Evento de pago publicado (sin fecha → sigue vigente).
	require.NoError(t, db.Create(&models.Event{
		BaseModel: models.BaseModel{ID: "ev3"}, TenantID: "t1", Title: "Diplomado",
		Modality: models.EventModalityVirtual, Status: models.EventStatusPublicado, Price: 100000,
	}).Error)
	require.NoError(t, db.Create(&models.Customer{
		BaseModel: models.BaseModel{ID: "c3"}, TenantID: "t1", Name: "Cata", Email: "cata@example.com",
	}).Error)
	// Saldo pendiente: pagó 40.000 de 100.000.
	require.NoError(t, db.Create(&models.EventRegistration{
		BaseModel: models.BaseModel{ID: "r3"}, TenantID: "t1", EventID: "ev3", CustomerID: "c3",
		QRToken: "q3", PublicToken: "p3",
		PaymentStatus: models.RegistrationPaymentPending, AmountPaid: 40000,
	}).Error)
	// Otro inscrito ya pagó completo → NO debe recibir recordatorio.
	require.NoError(t, db.Create(&models.Customer{
		BaseModel: models.BaseModel{ID: "c4"}, TenantID: "t1", Name: "Leo", Email: "leo@example.com",
	}).Error)
	require.NoError(t, db.Create(&models.EventRegistration{
		BaseModel: models.BaseModel{ID: "r4"}, TenantID: "t1", EventID: "ev3", CustomerID: "c4",
		QRToken: "q4", PublicToken: "p4",
		PaymentStatus: models.RegistrationPaymentConfirmed, AmountPaid: 100000,
	}).Error)

	fake := &email.FakeSender{}
	svc := email.NewServiceWithSender(fake, "eventos@vendia.store")

	res, err := jobs.RunEventReminders(db, now, nil, svc)
	require.NoError(t, err)
	assert.Equal(t, 1, res.QuotaRemindersSent, "solo el inscrito con saldo pendiente")
	require.Len(t, fake.Sent, 1)
	// El correo lleva el deep link con su token.
	assert.Contains(t, fake.Sent[0].Body, "dulce-ana/menu?reg=p3")
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
