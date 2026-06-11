// Spec: specs/042-modulo-eventos/spec.md
package database

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

func setupEventSalesDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.Customer{},
		&models.Sale{}, &models.SaleItem{},
	))
	return db
}

func seedEvt(t *testing.T, db *gorm.DB, tenant string, price, cost int64) *models.Event {
	t.Helper()
	e := &models.Event{TenantID: tenant, Title: "Curso", Price: price, Cost: cost, Status: models.EventStatusPublicado}
	require.NoError(t, db.Create(e).Error)
	return e
}

func seedReg(t *testing.T, db *gorm.DB, tenant, eventID, status string) *models.EventRegistration {
	t.Helper()
	cust := &models.Customer{TenantID: tenant, Name: "Asistente", Phone: "3001234567"}
	require.NoError(t, db.Create(cust).Error)
	reg := &models.EventRegistration{
		TenantID: tenant, EventID: eventID, CustomerID: cust.ID,
		PaymentStatus: status, PaymentMethod: "transferencia",
		QRToken: uuid.NewString(), PublicToken: uuid.NewString(),
	}
	require.NoError(t, db.Create(reg).Error)
	return reg
}

func TestBackfillEventSales_BooksConfirmedPaidOnly(t *testing.T) {
	db := setupEventSalesDB(t)
	paid := seedEvt(t, db, "t1", 60000, 10000)
	free := seedEvt(t, db, "t1", 0, 0)

	confirmed := seedReg(t, db, "t1", paid.ID, models.RegistrationPaymentConfirmed)
	_ = seedReg(t, db, "t1", paid.ID, models.RegistrationPaymentPending)   // pendiente → no
	_ = seedReg(t, db, "t1", free.ID, models.RegistrationPaymentConfirmed) // gratis → no

	created, err := BackfillEventSales(db)
	require.NoError(t, err)
	assert.Equal(t, 1, created)

	var sales []models.Sale
	require.NoError(t, db.Where("event_registration_id = ?", confirmed.ID).Find(&sales).Error)
	require.Len(t, sales, 1)
	assert.Equal(t, models.SaleSourceEvent, sales[0].Source)
	assert.Equal(t, float64(60000), sales[0].Total)
	assert.Equal(t, float64(10000), sales[0].CostAmount)
	// Fecha de la venta = fecha de la inscripción (ingreso histórico correcto).
	assert.WithinDuration(t, confirmed.CreatedAt, sales[0].CreatedAt, time.Second)

	// Total de ventas creadas = 1 (ni la pendiente ni la gratis generaron).
	var total int64
	require.NoError(t, db.Model(&models.Sale{}).Count(&total).Error)
	assert.Equal(t, int64(1), total)
}

func TestBackfillEventSales_Idempotent(t *testing.T) {
	db := setupEventSalesDB(t)
	ev := seedEvt(t, db, "t1", 60000, 0)
	seedReg(t, db, "t1", ev.ID, models.RegistrationPaymentConfirmed)

	first, err := BackfillEventSales(db)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	// Segunda corrida → no duplica (ya tiene venta).
	second, err := BackfillEventSales(db)
	require.NoError(t, err)
	assert.Equal(t, 0, second)

	var total int64
	require.NoError(t, db.Model(&models.Sale{}).Count(&total).Error)
	assert.Equal(t, int64(1), total)
}
