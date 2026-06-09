// Spec: specs/042-modulo-eventos/spec.md
package models

import "testing"

func TestEvent_Validate(t *testing.T) {
	base := func() Event {
		return Event{
			TenantID: "11111111-1111-1111-1111-111111111111",
			Type:     EventTypeCurso,
			Title:    "Taller de panadería",
			Modality: EventModalityPresencial,
			Capacity: 30,
			Price:    50000,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr bool
	}{
		{"válido", func(*Event) {}, false},
		{"precio 0 permitido (evento gratis)", func(e *Event) { e.Price = 0 }, false},
		{"sin tenant", func(e *Event) { e.TenantID = "" }, true},
		{"sin título", func(e *Event) { e.Title = "" }, true},
		{"tipo inválido", func(e *Event) { e.Type = "fiesta" }, true},
		{"modalidad inválida", func(e *Event) { e.Modality = "telepatía" }, true},
		{"precio negativo", func(e *Event) { e.Price = -50 }, true},
		{"precio no múltiplo de $50", func(e *Event) { e.Price = 50025 }, true},
		{"cupo negativo", func(e *Event) { e.Capacity = -1 }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := base()
			tt.mutate(&e)
			err := e.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() esperaba error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() inesperado: %v", err)
			}
		})
	}
}

func TestEvent_DefaultStatus(t *testing.T) {
	e := Event{}
	if got := e.StatusOrDefault(); got != EventStatusBorrador {
		t.Fatalf("StatusOrDefault() = %q, quería %q", got, EventStatusBorrador)
	}
	e.Status = EventStatusPublicado
	if got := e.StatusOrDefault(); got != EventStatusPublicado {
		t.Fatalf("StatusOrDefault() = %q, quería %q", got, EventStatusPublicado)
	}
}
