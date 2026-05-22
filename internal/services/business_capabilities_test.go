// Spec: specs/036-dashboard-adaptativo-onboarding/spec.md
package services_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
)

// TestDefaultCapabilitiesForType is the tabulated contract for the
// type→capabilities default map (Spec F036 §4.2). It pins the exact set
// of pre-activated capabilities for each of the 9 canonical business
// types. The map is the DEFAULT applied at registration only — it is
// NOT a restriction; any capability stays activable by any type via the
// normal PATCH /store/profile flow.
func TestDefaultCapabilitiesForType(t *testing.T) {
	tests := []struct {
		name string
		typ  string
		want services.Capabilities
	}{
		{
			name: "tienda_barrio → solo core",
			typ:  models.BusinessTypeTiendaBarrio,
			want: services.Capabilities{},
		},
		{
			name: "minimercado → solo core",
			typ:  models.BusinessTypeMinimercado,
			want: services.Capabilities{},
		},
		{
			name: "restaurante → recetas + mesas + servicios",
			typ:  models.BusinessTypeRestaurante,
			want: services.Capabilities{Recipes: true, Tables: true, Services: true},
		},
		{
			name: "comidas_rapidas → recetas + mesas + servicios",
			typ:  models.BusinessTypeComidasRapidas,
			want: services.Capabilities{Recipes: true, Tables: true, Services: true},
		},
		{
			name: "bar → mesas + servicios",
			typ:  models.BusinessTypeBar,
			want: services.Capabilities{Tables: true, Services: true},
		},
		{
			name: "deposito_construccion → cotizaciones + price tiers + clientes",
			typ:  models.BusinessTypeDepositoConstruccion,
			want: services.Capabilities{Quotes: true, PriceTiers: true, CustomerMgmt: true},
		},
		{
			name: "manufactura → cotizaciones + clientes + trabajos de muebles",
			typ:  models.BusinessTypeManufactura,
			want: services.Capabilities{Quotes: true, CustomerMgmt: true, FurnitureJobs: true},
		},
		{
			name: "reparacion_muebles → cotizaciones + clientes + trabajos de muebles",
			typ:  models.BusinessTypeReparacionMuebles,
			want: services.Capabilities{Quotes: true, CustomerMgmt: true, FurnitureJobs: true},
		},
		{
			name: "emprendimiento_general → clientes",
			typ:  models.BusinessTypeEmprendimientoGen,
			want: services.Capabilities{CustomerMgmt: true},
		},
		{
			name: "tipo desconocido → solo core (fallback seguro)",
			typ:  "tipo_inexistente",
			want: services.Capabilities{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := services.DefaultCapabilitiesForType(tc.typ)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestDefaultCapabilitiesForTypes verifies the multi-type union helper:
// a tenant registered with several types gets the OR of every type's
// defaults — capabilities only add, never cancel each other.
func TestDefaultCapabilitiesForTypes(t *testing.T) {
	got := services.DefaultCapabilitiesForTypes([]string{
		models.BusinessTypeRestaurante,
		models.BusinessTypeDepositoConstruccion,
	})
	want := services.Capabilities{
		Recipes:      true, // restaurante
		Tables:       true, // restaurante
		Services:     true, // restaurante
		Quotes:       true, // deposito
		PriceTiers:   true, // deposito
		CustomerMgmt: true, // deposito
	}
	assert.Equal(t, want, got)
}

// TestDefaultCapabilitiesForTypes_Empty verifies the no-type case
// returns the empty (core-only) set rather than panicking.
func TestDefaultCapabilitiesForTypes_Empty(t *testing.T) {
	assert.Equal(t, services.Capabilities{}, services.DefaultCapabilitiesForTypes(nil))
}
