// Spec: specs/084-peluqueria-salon/spec.md
package services_test

import (
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
)

func f(v float64) *float64 { return &v }

// A-base / prorrateo: la suma de las partes == total exacto (sin perder pesos).
func TestProrateLargestRemainder_ConservesTotal(t *testing.T) {
	cases := []struct {
		total   float64
		weights []float64
	}{
		{100, []float64{1, 1, 1}},        // 100/3 → 34,33,33
		{2997, []float64{999, 999, 999}}, // exacto 999 c/u
		{10000, []float64{5000, 4500}},   // 5263,4737 aprox
		{7, []float64{3, 3, 3, 3}},       // residuo a los mayores
	}
	for _, c := range cases {
		parts := services.ProrateLargestRemainder(c.total, c.weights)
		var sum float64
		for _, p := range parts {
			sum += p
			assert.GreaterOrEqual(t, p, 0.0)
		}
		assert.Equal(t, c.total, sum, "suma de partes == total para %v", c)
	}
	// Peso cero no recibe nada.
	parts := services.ProrateLargestRemainder(100, []float64{0, 1})
	assert.Equal(t, 0.0, parts[0])
	assert.Equal(t, 100.0, parts[1])
}

// A-comision / resolución de tasa: persona > servicio > 0; congela monto.
func TestResolveLineCommission_RateResolutionAndFreeze(t *testing.T) {
	// pct de la persona gana sobre el del servicio.
	cfg := &models.EmployeePayConfig{PayModel: models.PayModelCommission, CommissionPct: f(40)}
	basis, pct, amount := services.ResolveLineCommission(cfg, f(20), 10000)
	assert.Equal(t, models.PayBasisCommission, basis)
	assert.Equal(t, 40.0, *pct)
	assert.Equal(t, 4000.0, amount) // 10000 * 40%

	// sin pct de persona → cae al del servicio.
	cfg2 := &models.EmployeePayConfig{PayModel: models.PayModelCommission}
	_, pct2, amount2 := services.ResolveLineCommission(cfg2, f(15), 10000)
	assert.Equal(t, 15.0, *pct2)
	assert.Equal(t, 1500.0, amount2)

	// sin pct en ninguno → 0.
	_, _, amount3 := services.ResolveLineCommission(cfg2, nil, 10000)
	assert.Equal(t, 0.0, amount3)

	// salary_commission también congela comisión por línea.
	cfgS := &models.EmployeePayConfig{PayModel: models.PayModelSalaryCommission, CommissionPct: f(10)}
	basisS, _, amountS := services.ResolveLineCommission(cfgS, nil, 20000)
	assert.Equal(t, models.PayBasisCommission, basisS)
	assert.Equal(t, 2000.0, amountS)

	// fixed / chair_rent → sin comisión de línea, PayBasis correcto.
	cfgF := &models.EmployeePayConfig{PayModel: models.PayModelFixedPerJob}
	bF, _, aF := services.ResolveLineCommission(cfgF, f(50), 10000)
	assert.Equal(t, models.PayBasisFixed, bF)
	assert.Equal(t, 0.0, aF)
	cfgR := &models.EmployeePayConfig{PayModel: models.PayModelChairRent}
	bR, _, aR := services.ResolveLineCommission(cfgR, f(50), 10000)
	assert.Equal(t, models.PayBasisChairRent, bR)
	assert.Equal(t, 0.0, aR)

	// cfg nil → none, 0.
	bN, pN, aN := services.ResolveLineCommission(nil, f(50), 10000)
	assert.Equal(t, models.PayBasisNone, bN)
	assert.Nil(t, pN)
	assert.Equal(t, 0.0, aN)
}

func lines() []services.PayrollLine {
	return []services.PayrollLine{
		{LineNet: 10000, PayBasis: models.PayBasisCommission, CommissionAmount: 4000, SaleID: "s1", Day: "2026-06-27"},
		{LineNet: 6000, PayBasis: models.PayBasisCommission, CommissionAmount: 2400, SaleID: "s1", Day: "2026-06-27"},
		{LineNet: 8000, PayBasis: models.PayBasisCommission, CommissionAmount: 3200, SaleID: "s2", Day: "2026-06-28"},
	}
}

// A-comision: neto = suma de comisiones congeladas (+propina).
func TestComputePayout_Commission(t *testing.T) {
	p := services.ComputePayout(lines(), services.PayrollContext{PayModel: models.PayModelCommission, TipRate: 1})
	assert.Equal(t, 24000.0, p.GrossServices)
	assert.Equal(t, 3, p.ServiceCount)
	assert.Equal(t, 9600.0, p.CommissionAmount) // 4000+2400+3200
	assert.Equal(t, 9600.0, p.NetPayout)
	assert.Equal(t, models.PayoutDirectionToPro, p.Direction)
}

// A-propina-excluida: la propina suma al neto vía tip, no multiplica la comisión.
func TestComputePayout_TipAddedNotMultiplied(t *testing.T) {
	ls := lines()
	ls[0].TipShare = 1000
	ls[2].TipShare = 500
	p := services.ComputePayout(ls, services.PayrollContext{PayModel: models.PayModelCommission, TipRate: 1})
	assert.Equal(t, 1500.0, p.TipAmount)
	assert.Equal(t, 9600.0+1500.0, p.NetPayout)
	// tip_rate 0.5 → la mitad de la propina.
	p2 := services.ComputePayout(ls, services.PayrollContext{PayModel: models.PayModelCommission, TipRate: 0.5})
	assert.Equal(t, 750.0, p2.TipAmount)
}

// A-sueldo-piso / equivalencia: base + comisión; con 0 líneas paga la base.
func TestComputePayout_SalaryCommission(t *testing.T) {
	p := services.ComputePayout(lines(), services.PayrollContext{
		PayModel: models.PayModelSalaryCommission, BaseSalary: 500000, TipRate: 1})
	assert.Equal(t, 500000.0, p.SalaryAmount)
	assert.Equal(t, 9600.0, p.CommissionAmount)
	assert.Equal(t, 509600.0, p.NetPayout)

	// Periodo sin trabajos → paga solo la base.
	empty := services.ComputePayout(nil, services.PayrollContext{
		PayModel: models.PayModelSalaryCommission, BaseSalary: 500000, TipRate: 1})
	assert.Equal(t, 500000.0, empty.NetPayout)
}

// A-fijo-invariante: fijo × jobcount según unidad; invariante al precio.
func TestComputePayout_FixedByUnit(t *testing.T) {
	perService := services.ComputePayout(lines(), services.PayrollContext{
		PayModel: models.PayModelFixedPerJob, FixedPerJob: 5000, FixedUnit: models.FixedUnitService, TipRate: 1})
	assert.Equal(t, 15000.0, perService.FixedAmount) // 3 servicios × 5000

	perTicket := services.ComputePayout(lines(), services.PayrollContext{
		PayModel: models.PayModelFixedPerJob, FixedPerJob: 5000, FixedUnit: models.FixedUnitTicket, TipRate: 1})
	assert.Equal(t, 10000.0, perTicket.FixedAmount) // 2 tickets (s1,s2)

	perDay := services.ComputePayout(lines(), services.PayrollContext{
		PayModel: models.PayModelFixedPerJob, FixedPerJob: 5000, FixedUnit: models.FixedUnitDay, TipRate: 1})
	assert.Equal(t, 10000.0, perDay.FixedAmount) // 2 días distintos
}

// A-arriendo: renta independiente del ingreso; neto puede ser negativo (deuda).
func TestComputePayout_ChairRent(t *testing.T) {
	// who=shop: el salón retuvo la caja → entrega recaudo - renta.
	shop := services.ComputePayout(lines(), services.PayrollContext{
		PayModel: models.PayModelChairRent, RentRate: 30000, RentUnits: 1, WhoCollects: models.WhoCollectsShop, TipRate: 1})
	assert.Equal(t, 30000.0, shop.ChairRentAmount)
	assert.Equal(t, 24000.0-30000.0, shop.NetPayout) // -6000 → el pro debe al salón
	assert.Equal(t, models.PayoutDirectionToSalon, shop.Direction)

	// who=pro: el pro ya tiene la caja → debe la renta.
	pro := services.ComputePayout(lines(), services.PayrollContext{
		PayModel: models.PayModelChairRent, RentRate: 30000, RentUnits: 2, WhoCollects: models.WhoCollectsPro, TipRate: 1})
	assert.Equal(t, 60000.0, pro.ChairRentAmount)
	assert.Equal(t, -60000.0, pro.NetPayout)
	assert.Equal(t, models.PayoutDirectionToSalon, pro.Direction)

	// Periodo SIN trabajos igual debe la renta.
	idle := services.ComputePayout(nil, services.PayrollContext{
		PayModel: models.PayModelChairRent, RentRate: 30000, RentUnits: 1, WhoCollects: models.WhoCollectsShop, TipRate: 1})
	assert.Equal(t, -30000.0, idle.NetPayout)
}

// A-equivalencia: salary_commission con base 0 == commission peso a peso.
func TestComputePayout_SalaryZeroEqualsCommission(t *testing.T) {
	a := services.ComputePayout(lines(), services.PayrollContext{PayModel: models.PayModelCommission, TipRate: 1})
	b := services.ComputePayout(lines(), services.PayrollContext{PayModel: models.PayModelSalaryCommission, BaseSalary: 0, TipRate: 1})
	assert.Equal(t, a.NetPayout, b.NetPayout)
}
