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

func setupCheckinDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.EventScan{},
		&models.Customer{},
	))
	return db
}

// seedConfirmedRegistration returns a confirmed registration (free event) with
// a QR token ready for check-in.
func seedConfirmedRegistration(t *testing.T, db *gorm.DB, tenantID string) (*models.Event, *models.EventRegistration) {
	t.Helper()
	evSvc := services.NewEventService(db)
	ev, err := evSvc.Create(tenantID, &models.Event{
		Type: models.EventTypeCurso, Title: "Taller", Modality: models.EventModalityPresencial,
		Capacity: 10, Price: 0, AttendanceRule: models.AttendanceRuleInOut,
	})
	require.NoError(t, err)
	_, err = evSvc.Publish(tenantID, ev.ID)
	require.NoError(t, err)

	reg, err := services.NewEventRegistrationService(db).Register(tenantID, services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)
	return ev, reg
}

func TestRecordScan_Idempotent(t *testing.T) {
	db := setupCheckinDB(t)
	_, reg := seedConfirmedRegistration(t, db, "tenant-a")
	svc := services.NewEventCheckinService(db)

	_, created1, err := svc.RecordScan("tenant-a", reg.QRToken, models.ScanTypeIn, 0, "user-1")
	require.NoError(t, err)
	assert.True(t, created1)

	// Re-scanning the same QR / type / session is a no-op (decision R-03).
	_, created2, err := svc.RecordScan("tenant-a", reg.QRToken, models.ScanTypeIn, 0, "user-1")
	require.NoError(t, err)
	assert.False(t, created2, "el reescaneo no debe crear otra fila")

	var n int64
	require.NoError(t, db.Model(&models.EventScan{}).
		Where("registration_id = ? AND scan_type = ?", reg.ID, models.ScanTypeIn).Count(&n).Error)
	assert.Equal(t, int64(1), n)
}

func TestRecordScan_UnknownQR(t *testing.T) {
	db := setupCheckinDB(t)
	_, _ = seedConfirmedRegistration(t, db, "tenant-a")
	svc := services.NewEventCheckinService(db)

	_, _, err := svc.RecordScan("tenant-a", "00000000-0000-4000-8000-000000000000", models.ScanTypeIn, 0, "user-1")
	assert.Error(t, err)
}

func TestEligibility_InOutRule(t *testing.T) {
	db := setupCheckinDB(t)
	_, reg := seedConfirmedRegistration(t, db, "tenant-a")
	svc := services.NewEventCheckinService(db)

	// Only entrada → not eligible yet.
	_, _, err := svc.RecordScan("tenant-a", reg.QRToken, models.ScanTypeIn, 0, "user-1")
	require.NoError(t, err)
	var afterIn models.EventRegistration
	require.NoError(t, db.First(&afterIn, "id = ?", reg.ID).Error)
	assert.False(t, afterIn.CertificateEligible)

	// Entrada + salida → eligible (decision #3).
	_, _, err = svc.RecordScan("tenant-a", reg.QRToken, models.ScanTypeOut, 0, "user-1")
	require.NoError(t, err)
	var afterOut models.EventRegistration
	require.NoError(t, db.First(&afterOut, "id = ?", reg.ID).Error)
	assert.True(t, afterOut.CertificateEligible)
}
