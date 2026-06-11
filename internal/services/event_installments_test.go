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

func TestBuildInstallmentPlan_FirstAtRegistrationLastAtStart(t *testing.T) {
	reg := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	start := reg.Add(30 * 24 * time.Hour)
	// Recién inscrito, nada pagado.
	plan := services.BuildInstallmentPlan(reg, &start, 3, 60000, 0, reg)
	require.NotNil(t, plan)
	assert.Equal(t, 3, plan.Count)
	assert.Len(t, plan.Cuotas, 3)
	// 1ª vence al inscribirse, 3ª al iniciar el evento.
	assert.True(t, plan.Cuotas[0].DueDate.Equal(reg))
	assert.True(t, plan.Cuotas[2].DueDate.Equal(start))
	// 2ª a la mitad.
	assert.True(t, plan.Cuotas[1].DueDate.Equal(reg.Add(15*24*time.Hour)))
	// Suman exactamente el precio.
	var sum int64
	for _, c := range plan.Cuotas {
		sum += c.Amount
	}
	assert.Equal(t, int64(60000), sum)
	assert.Equal(t, 0, plan.OverdueCount)
	assert.Equal(t, 1, plan.NextDueNumber)
}

func TestBuildInstallmentPlan_OverdueAndPaidCounts(t *testing.T) {
	reg := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	start := reg.Add(30 * 24 * time.Hour)
	now := reg.Add(20 * 24 * time.Hour) // pasaron 20 días

	// Nada pagado → cuota 1 (día 0) y cuota 2 (día 15) vencidas; cuota 3 (día 30) pendiente.
	plan := services.BuildInstallmentPlan(reg, &start, 3, 60000, 0, now)
	require.NotNil(t, plan)
	assert.Equal(t, 2, plan.OverdueCount)
	assert.Equal(t, int64(40000), plan.OverdueAmount)
	assert.Equal(t, 1, plan.NextDueNumber)
	assert.Equal(t, 0, plan.PaidCount)

	// Pagó la 1ª cuota → siguiente vencida es la 2ª; solo 1 vencida.
	plan = services.BuildInstallmentPlan(reg, &start, 3, 60000, 20000, now)
	require.NotNil(t, plan)
	assert.Equal(t, 1, plan.PaidCount)
	assert.Equal(t, 1, plan.OverdueCount)
	assert.Equal(t, 2, plan.NextDueNumber)
}

func TestBuildInstallmentPlan_NilWhenNotApplicable(t *testing.T) {
	reg := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	start := reg.Add(30 * 24 * time.Hour)
	assert.Nil(t, services.BuildInstallmentPlan(reg, &start, 1, 60000, 0, reg)) // <2 cuotas
	assert.Nil(t, services.BuildInstallmentPlan(reg, &start, 3, 0, 0, reg))     // precio 0
}
