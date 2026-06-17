// Spec: specs/063-control-edad-18/spec.md
package handlers

import (
	"testing"
	"time"
)

// now fijo: 2026-06-16.
var ageNow = time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

func TestComputeAgeISO(t *testing.T) {
	cases := []struct {
		name    string
		birth   string
		wantAge int
		wantOK  bool
	}{
		{"cumpleaños hoy = 18", "2008-06-16", 18, true},
		{"un día antes de cumplir 18 = 17", "2008-06-17", 17, true},
		{"cumpleaños ya pasó este año", "2008-06-15", 18, true},
		{"persona mayor", "1980-01-01", 46, true},
		{"cadena vacía", "", 0, false},
		{"formato inválido", "16/06/2008", 0, false},
		{"día inexistente", "2020-02-31", 0, false},
		{"mes inexistente", "2020-13-01", 0, false},
		{"fecha futura", "2030-01-01", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			age, ok := computeAgeISO(tc.birth, ageNow)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, quería %v", ok, tc.wantOK)
			}
			if ok && age != tc.wantAge {
				t.Fatalf("edad = %d, quería %d", age, tc.wantAge)
			}
		})
	}
}

func TestIsAdultISO(t *testing.T) {
	cases := []struct {
		birth string
		want  bool
	}{
		{"2008-06-16", true},  // exactamente 18 hoy
		{"2008-06-17", false}, // 17, cumple mañana
		{"1990-12-31", true},  // mayor
		{"", false},           // fail-closed
		{"basura", false},     // fail-closed
		{"2030-01-01", false}, // futura → fail-closed
	}
	for _, tc := range cases {
		if got := isAdultISO(tc.birth, ageNow); got != tc.want {
			t.Errorf("isAdultISO(%q) = %v, quería %v", tc.birth, got, tc.want)
		}
	}
}
