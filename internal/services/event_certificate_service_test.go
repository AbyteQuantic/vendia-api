// Spec: specs/042-modulo-eventos/spec.md
package services_test

import (
	"testing"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupCertDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.EventScan{}, &models.Customer{},
	))
	return db
}

func TestIssueCertificate_RequiresEligibility(t *testing.T) {
	db := setupCertDB(t)
	tenantID := "tenant-a"
	ev, err := services.NewEventService(db).Create(tenantID, &models.Event{
		Type: models.EventTypeCurso, Title: "Curso", Modality: models.EventModalityVirtual, Price: 0,
	})
	require.NoError(t, err)

	reg := &models.EventRegistration{
		TenantID: tenantID, EventID: ev.ID, CustomerID: "c1",
		QRToken: "66666666-6666-4666-8666-666666666666", PublicToken: "77777777-7777-4777-8777-777777777777",
		PaymentStatus: models.RegistrationPaymentConfirmed, CertificateEligible: false,
	}
	require.NoError(t, db.Create(reg).Error)

	svc := services.NewEventCertificateService(db)
	_, err = svc.Issue(tenantID, reg.ID)
	assert.ErrorIs(t, err, services.ErrCertificateNotEligible)

	// Make eligible → issuance succeeds and is idempotent.
	require.NoError(t, db.Model(reg).Update("certificate_eligible", true).Error)
	issued, err := svc.Issue(tenantID, reg.ID)
	require.NoError(t, err)
	require.NotNil(t, issued.CertificateIssuedAt)

	first := *issued.CertificateIssuedAt
	again, err := svc.Issue(tenantID, reg.ID)
	require.NoError(t, err)
	assert.Equal(t, first, *again.CertificateIssuedAt, "la reemisión es idempotente")
}

func TestIssueAllEligible_OnlyEligibleNotIssued(t *testing.T) {
	db := setupCertDB(t)
	tenantID := "tenant-a"
	ev, err := services.NewEventService(db).Create(tenantID, &models.Event{
		Type: models.EventTypeCurso, Title: "Curso", Modality: models.EventModalityVirtual, Price: 0,
	})
	require.NoError(t, err)

	mk := func(id string, eligible bool, issued bool) {
		r := &models.EventRegistration{
			TenantID: tenantID, EventID: ev.ID, CustomerID: id,
			QRToken: id + "-qr", PublicToken: id + "-pt",
			CertificateEligible: eligible,
		}
		if issued {
			now := time.Now().UTC()
			r.CertificateIssuedAt = &now
		}
		require.NoError(t, db.Create(r).Error)
	}
	mk("a", true, false)  // elegible, sin emitir → SÍ
	mk("b", true, false)  // elegible, sin emitir → SÍ
	mk("c", true, true)   // ya emitido → NO
	mk("d", false, false) // no elegible (sin salida) → NO

	n, err := services.NewEventCertificateService(db).IssueAllEligible(tenantID, ev.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	var total int64
	db.Model(&models.EventRegistration{}).
		Where("event_id = ? AND certificate_issued_at IS NOT NULL", ev.ID).
		Count(&total)
	assert.Equal(t, int64(3), total) // a, b (nuevos) + c (ya estaba)
}
