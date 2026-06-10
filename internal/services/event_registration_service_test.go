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

func setupRegistrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.EventScan{},
		&models.Customer{},
	))
	return db
}

// seedPublishedEvent creates a published event with the given price/capacity.
func seedPublishedEvent(t *testing.T, db *gorm.DB, tenantID string, price int64, capacity int) *models.Event {
	t.Helper()
	svc := services.NewEventService(db)
	e := &models.Event{
		Type:     models.EventTypeCurso,
		Title:    "Curso",
		Modality: models.EventModalityVirtual,
		Capacity: capacity,
		Price:    price,
	}
	created, err := svc.Create(tenantID, e)
	require.NoError(t, err)
	_, err = svc.Publish(tenantID, created.ID)
	require.NoError(t, err)
	return created
}

func TestRegister_ConsentRequired(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 100000, 10)
	svc := services.NewEventRegistrationService(db)

	_, err := svc.Register("tenant-a", services.RegisterInput{
		EventID:      ev.ID,
		Name:         "Ana",
		Phone:        "3001234567",
		ConsentComms: false, // no consent → must be rejected
	})
	assert.Error(t, err)
}

func TestRegister_PaidEvent_PendingNoCupoConsumed(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 100000, 1)
	svc := services.NewEventRegistrationService(db)

	reg, err := svc.Register("tenant-a", services.RegisterInput{
		EventID:      ev.ID,
		Name:         "Ana",
		Phone:        "3001234567",
		ConsentComms: true,
	})
	require.NoError(t, err)
	assert.Equal(t, models.RegistrationPaymentPending, reg.PaymentStatus)
	assert.NotEmpty(t, reg.QRToken)
	assert.NotEmpty(t, reg.PublicToken)
	assert.NotEmpty(t, reg.CustomerID)
	assert.NotNil(t, reg.ConsentCommsAt)

	// A pending registration does NOT consume the single cupo.
	count, err := svc.CountConfirmed("tenant-a", ev.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestRegister_FreeEvent_ConfirmedImmediately(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 0, 10)
	svc := services.NewEventRegistrationService(db)

	reg, err := svc.Register("tenant-a", services.RegisterInput{
		EventID:      ev.ID,
		Name:         "Free Attendee",
		Phone:        "3009998888",
		ConsentComms: true,
	})
	require.NoError(t, err)
	assert.Equal(t, models.RegistrationPaymentConfirmed, reg.PaymentStatus)
}

func TestRegister_DedupesCustomerByPhone(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 0, 10)
	svc := services.NewEventRegistrationService(db)

	r1, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)

	ev2 := seedPublishedEvent(t, db, "tenant-a", 0, 10)
	r2, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev2.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)

	assert.Equal(t, r1.CustomerID, r2.CustomerID, "mismo teléfono → mismo Customer")

	var customers int64
	require.NoError(t, db.Model(&models.Customer{}).Where("tenant_id = ?", "tenant-a").Count(&customers).Error)
	assert.Equal(t, int64(1), customers)
}

// La misma persona (mismo teléfono) inscribiéndose dos veces al MISMO evento
// no genera duplicado: la segunda llamada devuelve su inscripción existente
// (la "logea" en su suscripción). FR-07 / pedido del dueño.
func TestRegister_SamePersonSameEvent_ReturnsExistingNoDuplicate(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 100000, 10) // pago → queda pending
	svc := services.NewEventRegistrationService(db)

	r1, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)

	r2, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)

	assert.Equal(t, r1.ID, r2.ID,
		"re-inscribirse al mismo evento devuelve la MISMA inscripción")

	var regs int64
	require.NoError(t, db.Model(&models.EventRegistration{}).
		Where("tenant_id = ? AND event_id = ?", "tenant-a", ev.ID).
		Count(&regs).Error)
	assert.Equal(t, int64(1), regs, "no debe duplicar la inscripción")
}

func TestConfirmPayment_ReservesCupoAndEnforcesCapacity(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 100000, 1) // capacity 1
	svc := services.NewEventRegistrationService(db)

	r1, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Uno", Phone: "3001111111", ConsentComms: true,
	})
	require.NoError(t, err)
	r2, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Dos", Phone: "3002222222", ConsentComms: true,
	})
	require.NoError(t, err)

	// First confirmation takes the only cupo.
	confirmed, err := svc.ConfirmPayment("tenant-a", r1.ID)
	require.NoError(t, err)
	assert.Equal(t, models.RegistrationPaymentConfirmed, confirmed.PaymentStatus)

	// Second confirmation must be rejected — cupo agotado (AC-04).
	_, err = svc.ConfirmPayment("tenant-a", r2.ID)
	assert.ErrorIs(t, err, services.ErrEventCapacityFull)

	// Re-confirming an already-confirmed registration is idempotent.
	again, err := svc.ConfirmPayment("tenant-a", r1.ID)
	require.NoError(t, err)
	assert.Equal(t, models.RegistrationPaymentConfirmed, again.PaymentStatus)
}
