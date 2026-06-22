// Spec: specs/078-centro-tareas-unificado/spec.md
package models

import "time"

// TaskKind — discriminador de la fuente de una tarea (deriva de una entidad real).
const (
	TaskOnlineOrder    = "online_order"
	TaskTableAccount   = "table_account"
	TaskErrand         = "errand"
	TaskReorder        = "reorder"
	TaskPerishable     = "perishable"
	TaskMenuIncomplete = "menu_incomplete"
)

// Urgencias (orden y color en el cliente).
const (
	TaskUrgencyCritical = "critical"
	TaskUrgencyHigh     = "high"
	TaskUrgencyNormal   = "normal"
	TaskUrgencyLow      = "low"
)

// Task — value object DERIVADO (NO una tabla). El Centro de Tareas lo arma en
// lectura a partir de las entidades dueñas (pedidos, órdenes, mandados, productos);
// una tarea está abierta ⟺ su entidad sigue abierta. Spec 078.
type Task struct {
	ID          string    `json:"id"`              // INVARIANTE: "{kind}:{source_id}" (anti-duplicado)
	Kind        string    `json:"kind"`            // TaskKind
	SourceID    string    `json:"source_id"`       // id de la entidad real (order/errand/product/tenant)
	Title       string    `json:"title"`           // copy USTED neutral
	Subtitle    string    `json:"subtitle"`        // "hace 5 min", "$38.000 por cobrar", "vence en 2 días"
	Urgency     string    `json:"urgency"`         // critical|high|normal|low
	Count       int       `json:"count,omitempty"` // tareas agregadas ("5 por reordenar")
	ActionLabel string    `json:"action_label"`    // verbo exacto: Cobrar/Aceptar/Comprar/Crear promo
	DeepLink    string    `json:"deep_link"`       // ruta destino (KDS/Cuaderno/Inventario/Pedidos)
	Amount      float64   `json:"amount,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// urgencyRank — para ordenar (mayor = más arriba).
func TaskUrgencyRank(u string) int {
	switch u {
	case TaskUrgencyCritical:
		return 3
	case TaskUrgencyHigh:
		return 2
	case TaskUrgencyNormal:
		return 1
	default:
		return 0
	}
}

// IsActionable — cuenta para el badge (urgente o importante, no informativa).
func (t Task) IsActionable() bool {
	return t.Urgency == TaskUrgencyCritical || t.Urgency == TaskUrgencyHigh || t.Urgency == TaskUrgencyNormal
}
