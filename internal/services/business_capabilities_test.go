// Spec: specs/036-dashboard-adaptativo-onboarding/spec.md
// Spec: specs/037-reel-capacidades-dashboard/spec.md
package services_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
)

// TestDefaultCapabilitiesForType is the tabulated contract for the
// type→capabilities default map.
//
// Spec F036 §4.2 originally pre-activated specific capabilities per
// business_type at registration. Spec F037 reverts that: every type now
// resolves to the empty (core-only) set so the merchant arrives at a
// minimal Dashboard and discovers extra modules through the reel. The
// function stays alive for forward compatibility — should we ever return
// to type-based defaults, only the switch body changes.
func TestDefaultCapabilitiesForType(t *testing.T) {
	allTypes := []struct {
		name string
		typ  string
	}{
		{"tienda_barrio → solo core (F037)", models.BusinessTypeTiendaBarrio},
		{"minimercado → solo core (F037)", models.BusinessTypeMinimercado},
		{"restaurante → solo core (F037)", models.BusinessTypeRestaurante},
		{"comidas_rapidas → solo core (F037)", models.BusinessTypeComidasRapidas},
		{"bar → solo core (F037)", models.BusinessTypeBar},
		{"deposito_construccion → solo core (F037)", models.BusinessTypeDepositoConstruccion},
		{"manufactura → solo core (F037)", models.BusinessTypeManufactura},
		{"reparacion_muebles → solo core (F037)", models.BusinessTypeReparacionMuebles},
		{"emprendimiento_general → solo core (F037)", models.BusinessTypeEmprendimientoGen},
		{"tipo desconocido → solo core (fallback seguro)", "tipo_inexistente"},
	}

	for _, tc := range allTypes {
		t.Run(tc.name, func(t *testing.T) {
			got := services.DefaultCapabilitiesForType(tc.typ)
			assert.Equal(t, services.Capabilities{}, got,
				"F037: ningún tipo de negocio pre-activa capacidades opcionales")
		})
	}
}

// TestDefaultCapabilitiesForTypes verifies the multi-type union helper:
// under F037 every type resolves to Capabilities{}, so the OR of any
// combination is still empty. Function survives so its contract stays
// stable if we ever revert to type-based defaults.
func TestDefaultCapabilitiesForTypes(t *testing.T) {
	got := services.DefaultCapabilitiesForTypes([]string{
		models.BusinessTypeRestaurante,
		models.BusinessTypeDepositoConstruccion,
	})
	assert.Equal(t, services.Capabilities{}, got,
		"F037: la unión de múltiples tipos sigue siendo vacía")
}

// TestDefaultCapabilitiesForTypes_Empty verifies the no-type case
// returns the empty (core-only) set rather than panicking.
func TestDefaultCapabilitiesForTypes_Empty(t *testing.T) {
	assert.Equal(t, services.Capabilities{}, services.DefaultCapabilitiesForTypes(nil))
}
