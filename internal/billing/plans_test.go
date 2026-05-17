// Spec: specs/008-planes-suscripcion-epayco/spec.md
package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustTime(t *testing.T, rfc3339 string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	require.NoError(t, err)
	return parsed
}

func TestCatalog_ContainsFreeAndPro(t *testing.T) {
	cat := Catalog()
	require.Len(t, cat, 2, "catalogo debe tener exactamente Gratis y Pro")

	free := cat[0]
	assert.Equal(t, PlanFree, free.ID)
	assert.Equal(t, "Gratis", free.Name)
	assert.Equal(t, "COP", free.Currency)
	require.Len(t, free.Prices, 1, "el plan Gratis tiene una sola opcion de precio")
	assert.Equal(t, 0, free.Prices[0].Amount, "el plan Gratis cuesta $0")
	assert.Equal(t, IntervalMonthly, free.Prices[0].Interval)

	pro := cat[1]
	assert.Equal(t, PlanPro, pro.ID)
	assert.Equal(t, "Pro", pro.Name)
	assert.Equal(t, "COP", pro.Currency)
	require.Len(t, pro.Prices, 2, "el plan Pro tiene precio mensual y anual")
}

func TestLookupPrice_ProMonthly(t *testing.T) {
	price, err := LookupPrice(PlanPro, IntervalMonthly)
	require.NoError(t, err)
	assert.Equal(t, 29900, price.Amount, "Pro mensual = 29.900 COP")
	assert.Equal(t, "COP", price.Currency)
	assert.Equal(t, IntervalMonthly, price.Interval)
}

func TestLookupPrice_ProYearly(t *testing.T) {
	price, err := LookupPrice(PlanPro, IntervalYearly)
	require.NoError(t, err)
	assert.Equal(t, 299000, price.Amount, "Pro anual = 299.000 COP")
	assert.Equal(t, "COP", price.Currency)
	assert.Equal(t, IntervalYearly, price.Interval)
}

func TestLookupPrice_FreeMonthly(t *testing.T) {
	price, err := LookupPrice(PlanFree, IntervalMonthly)
	require.NoError(t, err)
	assert.Equal(t, 0, price.Amount)
}

func TestLookupPrice_RejectsUnknownPlan(t *testing.T) {
	_, err := LookupPrice("ENTERPRISE", IntervalMonthly)
	require.Error(t, err)
}

func TestLookupPrice_RejectsUnknownInterval(t *testing.T) {
	_, err := LookupPrice(PlanPro, "weekly")
	require.Error(t, err)
}

func TestLookupPrice_RejectsFreeYearly(t *testing.T) {
	// El plan Gratis no se cobra: solo expone la opcion mensual ($0).
	_, err := LookupPrice(PlanFree, IntervalYearly)
	require.Error(t, err)
}

func TestIsValidPlan(t *testing.T) {
	assert.True(t, IsValidPlan(PlanFree))
	assert.True(t, IsValidPlan(PlanPro))
	assert.False(t, IsValidPlan(""))
	assert.False(t, IsValidPlan("ENTERPRISE"))
}

func TestIsValidInterval(t *testing.T) {
	assert.True(t, IsValidInterval(IntervalMonthly))
	assert.True(t, IsValidInterval(IntervalYearly))
	assert.False(t, IsValidInterval(""))
	assert.False(t, IsValidInterval("daily"))
}

func TestExtendPeriod_Monthly(t *testing.T) {
	// +1 mes desde la base.
	base := mustTime(t, "2026-05-17T10:00:00Z")
	got := ExtendPeriod(base, IntervalMonthly)
	assert.Equal(t, mustTime(t, "2026-06-17T10:00:00Z"), got)
}

func TestExtendPeriod_Yearly(t *testing.T) {
	base := mustTime(t, "2026-05-17T10:00:00Z")
	got := ExtendPeriod(base, IntervalYearly)
	assert.Equal(t, mustTime(t, "2027-05-17T10:00:00Z"), got)
}

func TestExtendPeriod_UnknownIntervalReturnsBase(t *testing.T) {
	base := mustTime(t, "2026-05-17T10:00:00Z")
	// Un intervalo desconocido no debe mover el vencimiento.
	assert.Equal(t, base, ExtendPeriod(base, "weekly"))
	assert.Equal(t, base, ExtendPeriod(base, ""))
}
