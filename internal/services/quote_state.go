// Spec: specs/031-cotizaciones/spec.md
package services

import "vendia-backend/internal/models"

// quoteTransitions is the finite state machine of a Quote (Spec F031
// AC-05). A status NOT present as a key (or with an empty slice) is
// terminal — no outbound edge is allowed.
//
//	borrador   → enviada, reemplazada
//	enviada    → aprobada, rechazada, vencida, reemplazada
//	aprobada   → convertida
//	rechazada / vencida / convertida / reemplazada → terminal
var quoteTransitions = map[string][]string{
	models.QuoteStatusDraft: {
		models.QuoteStatusSent,
		models.QuoteStatusReplaced,
	},
	models.QuoteStatusSent: {
		models.QuoteStatusApproved,
		models.QuoteStatusRejected,
		models.QuoteStatusExpired,
		models.QuoteStatusReplaced,
	},
	models.QuoteStatusApproved: {
		models.QuoteStatusConverted,
	},
	// Terminal states intentionally omitted.
}

// CanTransition reports whether a Quote may move from status `from` to
// status `to`. Identity transitions (from == to) and transitions out of
// a terminal/unknown state are rejected.
func CanTransition(from, to string) bool {
	allowed, ok := quoteTransitions[from]
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		if candidate == to {
			return true
		}
	}
	return false
}
