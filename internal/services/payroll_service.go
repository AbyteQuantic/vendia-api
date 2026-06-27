// Spec: specs/084-peluqueria-salon/spec.md
//
// Motor de liquidación a profesionales (peluquería/barbería). Matemática PURA y
// determinista (sin DB): el handler consulta y arma los inputs; aquí solo se
// calcula. Dinero en COP enteros (math.Round a peso); prorrateos por
// LARGEST-REMAINDER para conservar el total exacto (sin perder/ganar pesos).
package services

import (
	"math"
	"sort"

	"vendia-backend/internal/models"
)

// roundCOP redondea a peso entero (COP no maneja centavos).
func roundCOP(v float64) float64 { return math.Round(v) }

// ProrateLargestRemainder reparte `total` (COP entero) entre `weights` de forma
// proporcional, garantizando que la suma de las partes == total exacto (el
// residuo por redondeo se asigna a los mayores remanentes). weights >= 0.
func ProrateLargestRemainder(total float64, weights []float64) []float64 {
	out := make([]float64, len(weights))
	var sumW float64
	for _, w := range weights {
		if w > 0 {
			sumW += w
		}
	}
	t := roundCOP(total)
	if sumW <= 0 || t == 0 {
		return out // todo 0
	}
	type frac struct {
		idx  int
		frac float64
	}
	var assigned float64
	fracs := make([]frac, 0, len(weights))
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		exact := t * w / sumW
		fl := math.Floor(exact)
		out[i] = fl
		assigned += fl
		fracs = append(fracs, frac{i, exact - fl})
	}
	// Reparte el residuo entero a los mayores remanentes fraccionarios.
	rem := int(roundCOP(t - assigned))
	sort.SliceStable(fracs, func(a, b int) bool { return fracs[a].frac > fracs[b].frac })
	for k := 0; k < rem && k < len(fracs); k++ {
		out[fracs[k].idx]++
	}
	return out
}

// ResolveLineCommission decide, AL COBRAR, el snapshot de comisión de una línea
// de servicio: qué modelo aplicó (PayBasis), la tasa y el monto CONGELADO. La
// liquidación luego SUMA estos montos (reproducible aunque cambien tarifas).
//
// Resolución de tasa (más específico gana): pct de la persona (cfg) → pct del
// servicio (product) → 0. lineNet = base del servicio (post-descuento, sin IVA,
// sin propina). Para fixed/chair_rent la comisión de línea es 0 (esos modelos se
// calculan en la liquidación a partir de conteos/renta).
func ResolveLineCommission(cfg *models.EmployeePayConfig, productPct *float64, lineNet float64) (payBasis string, pct *float64, amount float64) {
	if cfg == nil {
		return models.PayBasisNone, nil, 0
	}
	switch cfg.PayModel {
	case models.PayModelCommission, models.PayModelSalaryCommission:
		r := 0.0
		if cfg.CommissionPct != nil {
			r = *cfg.CommissionPct
		} else if productPct != nil {
			r = *productPct
		}
		rr := r
		return models.PayBasisCommission, &rr, roundCOP(lineNet * r / 100.0)
	case models.PayModelFixedPerJob:
		return models.PayBasisFixed, nil, 0
	case models.PayModelChairRent:
		return models.PayBasisChairRent, nil, 0
	default:
		return models.PayBasisNone, nil, 0
	}
}

// PayrollLine — una línea de servicio atribuida, ya con su comisión CONGELADA y
// su porción de propina prorrateada (las arma el handler desde la BD).
type PayrollLine struct {
	LineNet          float64 // base del servicio (sin IVA, sin propina)
	PayBasis         string  // snapshot
	CommissionAmount float64 // snapshot congelado
	TipShare         float64 // propina prorrateada a esta línea
	SaleID           string  // para contar tickets distintos
	Day              string  // "2026-06-27" para contar días distintos
}

// PayrollContext — parámetros del periodo y de la config del profesional que NO
// están congelados por línea (fijo/arriendo/sueldo se evalúan al liquidar).
type PayrollContext struct {
	PayModel    string
	FixedPerJob float64
	FixedUnit   string  // service|ticket|day
	BaseSalary  float64 // por periodo
	RentRate    float64
	RentUnits   float64 // unidades de arriendo en el periodo (días o semanas)
	WhoCollects string  // shop|pro
	TipRate     float64 // fracción de propina que se queda el pro (1.0 = 100%)
}

// Payout — resultado de la liquidación de UN profesional en el periodo.
type Payout struct {
	GrossServices    float64
	ServiceCount     int
	CommissionAmount float64
	FixedAmount      float64
	SalaryAmount     float64
	ChairRentAmount  float64 // renta debida (informativo); resta en el neto
	TipAmount        float64
	NetPayout        float64 // puede ser NEGATIVO (el pro debe al salón)
	Direction        string  // to_pro | to_salon
}

// ComputePayout calcula la liquidación de un profesional. Determinista.
func ComputePayout(lines []PayrollLine, ctx PayrollContext) Payout {
	var gross, commission, rawTip float64
	tickets := map[string]struct{}{}
	days := map[string]struct{}{}
	for _, l := range lines {
		gross += l.LineNet
		commission += l.CommissionAmount
		rawTip += l.TipShare
		if l.SaleID != "" {
			tickets[l.SaleID] = struct{}{}
		}
		if l.Day != "" {
			days[l.Day] = struct{}{}
		}
	}
	tipRate := ctx.TipRate
	if tipRate <= 0 {
		tipRate = 1.0
	}
	tip := roundCOP(rawTip * tipRate)

	p := Payout{
		GrossServices: roundCOP(gross),
		ServiceCount:  len(lines),
		TipAmount:     tip,
		Direction:     models.PayoutDirectionToPro,
	}

	switch ctx.PayModel {
	case models.PayModelCommission:
		p.CommissionAmount = roundCOP(commission)
		p.NetPayout = p.CommissionAmount + tip

	case models.PayModelSalaryCommission:
		p.SalaryAmount = roundCOP(ctx.BaseSalary)
		p.CommissionAmount = roundCOP(commission)
		p.NetPayout = p.SalaryAmount + p.CommissionAmount + tip

	case models.PayModelFixedPerJob:
		var jobs int
		switch ctx.FixedUnit {
		case models.FixedUnitTicket:
			jobs = len(tickets)
		case models.FixedUnitDay:
			jobs = len(days)
		default: // service
			jobs = len(lines)
		}
		p.FixedAmount = roundCOP(ctx.FixedPerJob * float64(jobs))
		p.NetPayout = p.FixedAmount + tip

	case models.PayModelChairRent:
		rentDue := roundCOP(ctx.RentRate * ctx.RentUnits)
		p.ChairRentAmount = rentDue
		// El profesional cobró el servicio + propina (o el salón lo retiene).
		collectedByPro := roundCOP(gross) + tip
		if ctx.WhoCollects == models.WhoCollectsPro {
			// El pro ya tiene su caja → le DEBE el arriendo al salón.
			p.NetPayout = -rentDue
			p.Direction = models.PayoutDirectionToSalon
		} else {
			// El salón retuvo la caja → le entrega al pro lo recaudado menos renta
			// (puede ser negativo: el pro debe al salón). NUNCA se clampa a 0.
			p.NetPayout = collectedByPro - rentDue
			if p.NetPayout < 0 {
				p.Direction = models.PayoutDirectionToSalon
			}
		}
	}
	return p
}
