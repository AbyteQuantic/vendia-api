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

func setupInstallmentsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.EventScan{},
		&models.Customer{}, &models.CreditAccount{}, &models.CreditPayment{},
		&models.EventInstallment{},
	))
	return db
}

func TestComputeInstallments_SumsExactlyAndMultipleOf50(t *testing.T) {
	cases := []struct {
		total int64
		n     int
	}{
		{150000, 3},
		{100000, 3}, // not evenly divisible — remainder distributed
		{50000, 1},
		{99950, 7},
	}
	for _, tc := range cases {
		schedule, err := services.ComputeInstallments(tc.total, tc.n)
		require.NoError(t, err, "total=%d n=%d", tc.total, tc.n)
		require.Len(t, schedule, tc.n)
		var sum int64
		for _, amt := range schedule {
			assert.Zero(t, amt%50, "cuota %d no es múltiplo de $50", amt)
			assert.Positive(t, amt)
			sum += amt
		}
		assert.Equal(t, tc.total, sum, "la suma de cuotas debe ser exactamente el precio")
	}
}

func TestComputeInstallments_TooManyInstallments(t *testing.T) {
	_, err := services.ComputeInstallments(100, 3) // only 2 units of $50
	assert.Error(t, err)
}

func TestSetupInstallments_CreatesSeparateLinkedAccount(t *testing.T) {
	db := setupInstallmentsDB(t)
	tenantID := "tenant-a"

	// Event with manual installments enabled.
	evSvc := services.NewEventService(db)
	ev, err := evSvc.Create(tenantID, &models.Event{
		Type: models.EventTypeCurso, Title: "Diplomado", Modality: models.EventModalityVirtual,
		Capacity: 50, Price: 150000, InstallmentsEnabled: true, InstallmentsCount: 3,
	})
	require.NoError(t, err)
	_, err = evSvc.Publish(tenantID, ev.ID)
	require.NoError(t, err)

	regSvc := services.NewEventRegistrationService(db)
	reg, err := regSvc.Register(tenantID, services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)

	account, schedule, err := regSvc.SetupInstallments(tenantID, reg.ID)
	require.NoError(t, err)
	require.NotNil(t, account)
	assert.Equal(t, int64(150000), account.TotalAmount)
	assert.Equal(t, reg.CustomerID, account.CustomerID)
	assert.Equal(t, "open", account.Status)
	require.Len(t, schedule, 3)

	// Registration now points at the event-scoped credit account (R-02).
	var stored models.EventRegistration
	require.NoError(t, db.First(&stored, "id = ?", reg.ID).Error)
	require.NotNil(t, stored.CreditAccountID)
	assert.Equal(t, account.ID, *stored.CreditAccountID)
}

func TestSetupInstallments_PersistsDatedSchedule(t *testing.T) {
	db := setupInstallmentsDB(t)
	tenantID := "tenant-a"

	evSvc := services.NewEventService(db)
	ev, err := evSvc.Create(tenantID, &models.Event{
		Type: models.EventTypeCurso, Title: "Diplomado", Modality: models.EventModalityVirtual,
		Capacity: 50, Price: 150000, InstallmentsEnabled: true, InstallmentsCount: 3,
	})
	require.NoError(t, err)
	_, err = evSvc.Publish(tenantID, ev.ID)
	require.NoError(t, err)

	regSvc := services.NewEventRegistrationService(db)
	reg, err := regSvc.Register(tenantID, services.RegisterInput{
		EventID: ev.ID, Name: "Ana", Phone: "3001234567", ConsentComms: true,
	})
	require.NoError(t, err)

	_, _, err = regSvc.SetupInstallments(tenantID, reg.ID)
	require.NoError(t, err)

	// D3: the dated schedule is persisted — one row per cuota, with due dates,
	// summing exactly to the price, all pending.
	var rows []models.EventInstallment
	require.NoError(t, db.Where("registration_id = ?", reg.ID).Order("number ASC").Find(&rows).Error)
	require.Len(t, rows, 3)

	var sum int64
	for i, r := range rows {
		assert.Equal(t, i+1, r.Number)
		assert.Equal(t, models.InstallmentStatusPending, r.Status)
		assert.Zero(t, r.Amount%50)
		assert.False(t, r.DueDate.IsZero(), "cada cuota debe tener fecha de vencimiento")
		if i > 0 {
			assert.True(t, r.DueDate.After(rows[i-1].DueDate), "las fechas deben ser crecientes")
		}
		sum += r.Amount
	}
	assert.Equal(t, int64(150000), sum)
}
