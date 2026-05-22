// Spec: specs/031-cotizaciones/spec.md
package services_test

import (
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
)

// TestCanTransition tabulates every (from → to) pair of the quote FSM
// (Spec F031 AC-05). The allowed edges are:
//
//	borrador  → enviada | reemplazada
//	enviada   → aprobada | rechazada | vencida | reemplazada
//	aprobada  → convertida
//	rechazada → (terminal)
//	vencida   → (terminal)
//	convertida → (terminal)
//	reemplazada → (terminal)
func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		// ── borrador ─────────────────────────────────────────────
		{"borrador→enviada", models.QuoteStatusDraft, models.QuoteStatusSent, true},
		{"borrador→reemplazada", models.QuoteStatusDraft, models.QuoteStatusReplaced, true},
		{"borrador→aprobada (no)", models.QuoteStatusDraft, models.QuoteStatusApproved, false},
		{"borrador→convertida (no)", models.QuoteStatusDraft, models.QuoteStatusConverted, false},
		{"borrador→vencida (no)", models.QuoteStatusDraft, models.QuoteStatusExpired, false},

		// ── enviada ──────────────────────────────────────────────
		{"enviada→aprobada", models.QuoteStatusSent, models.QuoteStatusApproved, true},
		{"enviada→rechazada", models.QuoteStatusSent, models.QuoteStatusRejected, true},
		{"enviada→vencida", models.QuoteStatusSent, models.QuoteStatusExpired, true},
		{"enviada→reemplazada", models.QuoteStatusSent, models.QuoteStatusReplaced, true},
		{"enviada→borrador (no)", models.QuoteStatusSent, models.QuoteStatusDraft, false},
		{"enviada→convertida (no)", models.QuoteStatusSent, models.QuoteStatusConverted, false},

		// ── aprobada ─────────────────────────────────────────────
		{"aprobada→convertida", models.QuoteStatusApproved, models.QuoteStatusConverted, true},
		{"aprobada→enviada (no)", models.QuoteStatusApproved, models.QuoteStatusSent, false},
		{"aprobada→rechazada (no)", models.QuoteStatusApproved, models.QuoteStatusRejected, false},

		// ── terminal states reject every outbound edge ───────────
		{"rechazada→enviada (no)", models.QuoteStatusRejected, models.QuoteStatusSent, false},
		{"rechazada→aprobada (no)", models.QuoteStatusRejected, models.QuoteStatusApproved, false},
		{"vencida→enviada (no)", models.QuoteStatusExpired, models.QuoteStatusSent, false},
		{"convertida→enviada (no)", models.QuoteStatusConverted, models.QuoteStatusSent, false},
		{"convertida→aprobada (no)", models.QuoteStatusConverted, models.QuoteStatusApproved, false},
		{"reemplazada→enviada (no)", models.QuoteStatusReplaced, models.QuoteStatusSent, false},

		// ── garbage / identity ───────────────────────────────────
		{"unknown from", "fantasma", models.QuoteStatusSent, false},
		{"unknown to", models.QuoteStatusDraft, "fantasma", false},
		{"identity borrador→borrador (no)", models.QuoteStatusDraft, models.QuoteStatusDraft, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := services.CanTransition(tt.from, tt.to)
			assert.Equal(t, tt.want, got,
				"CanTransition(%q, %q)", tt.from, tt.to)
		})
	}
}
