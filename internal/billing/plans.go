// Spec: specs/008-planes-suscripcion-epayco/spec.md
//
// Catalogo de planes de suscripcion de VendIA. Es config del backend
// (decision D4 del spec): NO es una tabla editable por UI en v1. Las
// constantes viven aqui porque el catalogo cambia con un deploy, no en
// caliente, y porque el cobro de dinero (Art. VII) debe ser exacto y
// auditable contra el codigo fuente, no contra una fila mutable.
package billing

import (
	"fmt"
	"time"
)

// Plan identifica un nivel de suscripcion.
const (
	// PlanFree — sin costo. El tenant tiene el POS basico.
	PlanFree = "FREE"
	// PlanPro — plan de pago con todas las funciones premium.
	PlanPro = "PRO"
)

// Interval identifica la cadencia de cobro de un plan de pago.
const (
	IntervalMonthly = "monthly"
	IntervalYearly  = "yearly"
)

// Precios del plan Pro en pesos colombianos (COP), enteros (Art. VII —
// cero coma flotante en dinero). Mensual 29.900 / anual 299.000.
const (
	priceProMonthlyCOP = 29900
	priceProYearlyCOP  = 299000
	currencyCOP        = "COP"
)

// Price es una opcion de precio concreta dentro de un plan.
type Price struct {
	Interval string `json:"interval"`
	Amount   int    `json:"amount"`   // entero, sin decimales (COP)
	Currency string `json:"currency"` // siempre "COP" en v1
}

// Plan es una entrada del catalogo expuesta a la UI.
type Plan struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"` // nombre de cara al usuario (espanol, Art. V)
	Currency string  `json:"currency"`
	Prices   []Price `json:"prices"`
}

// Catalog devuelve el catalogo completo de planes. Construye estructuras
// nuevas en cada llamada (Art. IX — inmutabilidad): un consumidor no
// puede mutar el catalogo global.
func Catalog() []Plan {
	return []Plan{
		{
			ID:       PlanFree,
			Name:     "Gratis",
			Currency: currencyCOP,
			Prices: []Price{
				{Interval: IntervalMonthly, Amount: 0, Currency: currencyCOP},
			},
		},
		{
			ID:       PlanPro,
			Name:     "Pro",
			Currency: currencyCOP,
			Prices: []Price{
				{Interval: IntervalMonthly, Amount: priceProMonthlyCOP, Currency: currencyCOP},
				{Interval: IntervalYearly, Amount: priceProYearlyCOP, Currency: currencyCOP},
			},
		},
	}
}

// LookupPrice resuelve el precio de un {plan, interval}. Devuelve error
// si el plan o el intervalo no existen, o si la combinacion no esta en
// el catalogo (p.ej. el plan Gratis no tiene opcion anual). El checkout
// usa esto como unica fuente del monto a cobrar.
func LookupPrice(plan, interval string) (Price, error) {
	if !IsValidPlan(plan) {
		return Price{}, fmt.Errorf("plan no valido: %q", plan)
	}
	if !IsValidInterval(interval) {
		return Price{}, fmt.Errorf("intervalo no valido: %q", interval)
	}
	for _, p := range Catalog() {
		if p.ID != plan {
			continue
		}
		for _, pr := range p.Prices {
			if pr.Interval == interval {
				return pr, nil
			}
		}
	}
	return Price{}, fmt.Errorf("combinacion plan/intervalo no disponible: %s/%s", plan, interval)
}

// IsValidPlan reporta si plan es un identificador conocido del catalogo.
func IsValidPlan(plan string) bool {
	return plan == PlanFree || plan == PlanPro
}

// IsValidInterval reporta si interval es una cadencia conocida.
func IsValidInterval(interval string) bool {
	return interval == IntervalMonthly || interval == IntervalYearly
}

// ExtendPeriod calcula el nuevo vencimiento al pagar un periodo: +1 mes
// o +1 ano desde base. Un intervalo desconocido devuelve base sin tocar
// (el llamador valida con IsValidInterval antes de pagar).
func ExtendPeriod(base time.Time, interval string) time.Time {
	switch interval {
	case IntervalMonthly:
		return base.AddDate(0, 1, 0)
	case IntervalYearly:
		return base.AddDate(1, 0, 0)
	default:
		return base
	}
}
