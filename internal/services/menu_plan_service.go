// Spec: specs/066-planear-menu/spec.md
package services

import (
	"strings"
	"time"
)

// CommerceTimeZone es la zona horaria del país del comercio usada para definir
// "hoy" al resolver el menú efectivo (Spec 066, AC-09). VendIA es solo Colombia
// (America/Bogota, UTC-5, sin DST). Centralizado aquí para extender a
// tenant.country sin tocar la lógica de resolución.
const CommerceTimeZone = "America/Bogota"

// weekdayKeys mapea time.Weekday (Sunday=0) a la clave usada en el JSONB del
// plan semanal. Mantener el orden alineado con time.Weekday.
var weekdayKeys = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// dayLabelsES — nombre del día en español para la etiqueta del link público
// ("Menú del jueves"). Indexado por clave de día.
var dayLabelsES = map[string]string{
	"mon": "lunes", "tue": "martes", "wed": "miércoles", "thu": "jueves",
	"fri": "viernes", "sat": "sábado", "sun": "domingo",
}

// MenuPlanItem — un plato planeado para un día. PlannedQty es guía interna.
type MenuPlanItem struct {
	RecipeUUID string `json:"recipe_uuid"`
	PlannedQty int    `json:"planned_qty"`
}

// DayPlan — el plan de un día (de la plantilla o de un override).
type DayPlan struct {
	Enabled bool           `json:"enabled"`
	Items   []MenuPlanItem `json:"items"`
}

// EffectiveMenu — resultado de resolver qué menú mostrar en el link público.
type EffectiveMenu struct {
	Found   bool           // hay un día habilitado con platos en los próximos 7 días
	IsToday bool           // el menú resuelto corresponde a hoy
	DayKey  string         // clave del día resuelto (mon…sun)
	Items   []MenuPlanItem // platos del día resuelto
}

// LoadTimezone devuelve la ubicación del comercio, con fallback a UTC si la
// base de datos de zonas horarias no está disponible (entornos mínimos).
func LoadTimezone() *time.Location {
	loc, err := time.LoadLocation(CommerceTimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// DayLabelES devuelve el nombre del día en español para la clave dada.
func DayLabelES(dayKey string) string {
	return dayLabelsES[dayKey]
}

// ResolveEffectiveMenu aplica la regla del spec (AC-08): recorre hasta 7 días
// desde `today`; para cada fecha el override gana sobre la plantilla del día de
// la semana; el primer día habilitado con platos gana. Si ningún día en 7 está
// habilitado con platos, devuelve Found=false (la sección de platos desaparece).
//
//   - days: plantilla semanal por clave de día (mon…sun).
//   - overrides: ajustes por fecha (clave = YYYY-MM-DD).
//   - today: fecha de referencia (ya en la zona del comercio).
func ResolveEffectiveMenu(days map[string]DayPlan, overrides map[string]DayPlan, today time.Time) EffectiveMenu {
	for offset := 0; offset < 7; offset++ {
		date := today.AddDate(0, 0, offset)
		dateKey := date.Format("2006-01-02")
		dayKey := weekdayKeys[int(date.Weekday())]

		dp, ok := overrides[dateKey]
		if !ok {
			dp, ok = days[dayKey]
		}
		if !ok {
			continue
		}
		if dp.Enabled && len(dp.Items) > 0 {
			return EffectiveMenu{
				Found:   true,
				IsToday: offset == 0,
				DayKey:  dayKey,
				Items:   dp.Items,
			}
		}
	}
	return EffectiveMenu{Found: false}
}

// MenuDayLabel construye la etiqueta del menú para el cliente: vacía si es hoy,
// o "Menú del <día>" cuando el menú mostrado corresponde a un día futuro.
func MenuDayLabel(m EffectiveMenu) string {
	if !m.Found || m.IsToday {
		return ""
	}
	label := DayLabelES(m.DayKey)
	if label == "" {
		return ""
	}
	return "Menú del " + label
}

// RecipeUUIDSet devuelve el conjunto de recipe_uuid del menú efectivo, útil
// para filtrar los productos-plato del catálogo público.
func RecipeUUIDSet(m EffectiveMenu) map[string]struct{} {
	set := make(map[string]struct{}, len(m.Items))
	for _, it := range m.Items {
		id := strings.TrimSpace(it.RecipeUUID)
		if id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}
