// Spec: specs/042-modulo-eventos/spec.md
package services_test

import (
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupRegListDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.EventScan{},
		&models.Customer{},
	))
	return db
}

func TestListByEvent_ReturnsAttendeesWithCustomerAndCheckin(t *testing.T) {
	db := setupRegListDB(t)
	tenantID := "tenant-a"

	ev, err := services.NewEventService(db).Create(tenantID, &models.Event{
		Type: models.EventTypeCurso, Title: "Curso", Modality: models.EventModalityVirtual,
		Capacity: 10, Price: 0,
	})
	require.NoError(t, err)
	_, err = services.NewEventService(db).Publish(tenantID, ev.ID)
	require.NoError(t, err)

	regSvc := services.NewEventRegistrationService(db)
	reg, err := regSvc.Register(tenantID, services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)

	// register a scan to mark checked-in
	_, _, err = services.NewEventCheckinService(db).RecordScan(
		tenantID, reg.QRToken, models.ScanTypeIn, 0, "user-1")
	require.NoError(t, err)

	views, err := regSvc.ListByEvent(tenantID, ev.ID)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "Ana", views[0].CustomerName)
	assert.Equal(t, "3001234567", views[0].CustomerPhone)
	assert.Equal(t, models.RegistrationPaymentConfirmed, views[0].PaymentStatus)
	assert.True(t, views[0].CheckedIn)
}

func TestListByEvent_TenantScoped(t *testing.T) {
	db := setupRegListDB(t)
	ev, err := services.NewEventService(db).Create("tenant-a", &models.Event{
		Type: models.EventTypeOtro, Title: "X", Modality: models.EventModalityPresencial,
	})
	require.NoError(t, err)

	views, err := services.NewEventRegistrationService(db).ListByEvent("tenant-b", ev.ID)
	require.NoError(t, err)
	assert.Empty(t, views)
}
