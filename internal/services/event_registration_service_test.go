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
		&models.Customer{}, &models.Sale{}, &models.SaleItem{},
	))
	return db
}

// eventSalesFor counts the ledger sales booked for a registration (Source=EVENT).
func eventSalesFor(t *testing.T, db *gorm.DB, regID string) []models.Sale {
	t.Helper()
	var sales []models.Sale
	require.NoError(t, db.Where("event_registration_id = ?", regID).Find(&sales).Error)
	return sales
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

// La silla se auto-asigna AL INSCRIBIRSE (silla más baja libre); el dueño
// puede liberarla y reasignarla, y una silla ocupada por otro se rechaza.
func TestSeatAutoAssignAndManualAssign(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 100000, 10)
	svc := services.NewEventRegistrationService(db)

	// Al inscribirse → silla 1 (sin esperar abono).
	r1, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001111111", ConsentComms: true,
	})
	require.NoError(t, err)
	require.NotNil(t, r1.SeatNumber, "la silla se asigna al inscribirse")
	assert.Equal(t, 1, *r1.SeatNumber)

	// Segundo asistente al inscribirse → silla 2.
	r2, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Beto", Phone: "3002222222", ConsentComms: true,
	})
	require.NoError(t, err)
	require.NotNil(t, r2.SeatNumber)
	assert.Equal(t, 2, *r2.SeatNumber)

	// Asignar a una silla ocupada por otro → error.
	seat1 := 1
	_, err = svc.AssignSeat("tenant-a", r2.ID, &seat1)
	assert.ErrorIs(t, err, services.ErrSeatTaken)

	// Liberar la silla de r1 y reasignar r2 a la 1.
	_, err = svc.AssignSeat("tenant-a", r1.ID, nil)
	require.NoError(t, err)
	moved, err := svc.AssignSeat("tenant-a", r2.ID, &seat1)
	require.NoError(t, err)
	require.NotNil(t, moved.SeatNumber)
	assert.Equal(t, 1, *moved.SeatNumber)

	// Fuera de capacidad → error.
	big := 99
	_, err = svc.AssignSeat("tenant-a", r1.ID, &big)
	assert.ErrorIs(t, err, services.ErrSeatInvalid)
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

func TestConfirmPayment_BooksEventSale_Idempotent(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 60000, 10)
	// Costo por asistente para verificar la ganancia.
	require.NoError(t, db.Model(&models.Event{}).Where("id = ?", ev.ID).
		Update("cost", int64(10000)).Error)
	svc := services.NewEventRegistrationService(db)

	reg, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
		PaymentMethod: "transferencia",
	})
	require.NoError(t, err)
	require.Empty(t, eventSalesFor(t, db, reg.ID)) // aún sin pagar → sin venta

	_, err = svc.ConfirmPayment("tenant-a", reg.ID)
	require.NoError(t, err)

	sales := eventSalesFor(t, db, reg.ID)
	require.Len(t, sales, 1)
	assert.Equal(t, models.SaleSourceEvent, sales[0].Source)
	assert.Equal(t, float64(60000), sales[0].Total)
	assert.Equal(t, float64(10000), sales[0].CostAmount) // ganancia = 60000-10000
	assert.Equal(t, models.PaymentTransfer, sales[0].PaymentMethod)

	// Idempotente: confirmar de nuevo no duplica la venta.
	_, err = svc.ConfirmPayment("tenant-a", reg.ID)
	require.NoError(t, err)
	assert.Len(t, eventSalesFor(t, db, reg.ID), 1)
}

func TestRecordPayment_BooksSaleOnlyWhenFullyPaid(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 60000, 10)
	svc := services.NewEventRegistrationService(db)

	reg, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Leo", Phone: "3009998888", ConsentComms: true,
	})
	require.NoError(t, err)

	// Abono parcial → todavía sin venta.
	_, err = svc.RecordPayment("tenant-a", reg.ID, 20000)
	require.NoError(t, err)
	assert.Empty(t, eventSalesFor(t, db, reg.ID))

	// Abono que completa el pago → se contabiliza UNA venta.
	_, err = svc.RecordPayment("tenant-a", reg.ID, 40000)
	require.NoError(t, err)
	sales := eventSalesFor(t, db, reg.ID)
	require.Len(t, sales, 1)
	assert.Equal(t, float64(60000), sales[0].Total)
}

func TestFreeEvent_DoesNotBookSale(t *testing.T) {
	db := setupRegistrationDB(t)
	ev := seedPublishedEvent(t, db, "tenant-a", 0, 10) // gratis
	svc := services.NewEventRegistrationService(db)

	reg, err := svc.Register("tenant-a", services.RegisterInput{
		EventID: ev.ID, Name: "Gratis", Phone: "3001112222", ConsentComms: true,
	})
	require.NoError(t, err)
	assert.True(t, reg.IsConfirmed())                 // gratis se confirma solo
	assert.Empty(t, eventSalesFor(t, db, reg.ID))     // pero NO genera venta ($0)
}
