// Spec: specs/023-capacidades-opcionales-negocio/spec.md
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultFeatureFlags(t *testing.T) {
	cases := []struct {
		name string
		types []string
		opts  CapabilityToggles
		want  FeatureFlags
	}{
		{
			name:  "tienda_barrio — nothing enabled",
			types: []string{BusinessTypeTiendaBarrio},
			opts:  CapabilityToggles{},
			want:  FeatureFlags{},
		},
		{
			name:  "restaurante enables food stack",
			types: []string{BusinessTypeRestaurante},
			opts:  CapabilityToggles{},
			want: FeatureFlags{
				EnableTables: true,
				EnableKDS:    true,
				EnableTips:   true,
			},
		},
		{
			name:  "bar + restaurante still deduplicates flags",
			types: []string{BusinessTypeBar, BusinessTypeRestaurante},
			opts:  CapabilityToggles{},
			want: FeatureFlags{
				EnableTables: true,
				EnableKDS:    true,
				EnableTips:   true,
			},
		},
		{
			name:  "reparacion_muebles enables services + custom billing",
			types: []string{BusinessTypeReparacionMuebles},
			opts:  CapabilityToggles{},
			want: FeatureFlags{
				EnableServices:      true,
				EnableCustomBilling: true,
			},
		},
		{
			name:  "deposito_construccion enables fractional units",
			types: []string{BusinessTypeDepositoConstruccion},
			opts:  CapabilityToggles{},
			want: FeatureFlags{
				EnableFractionalUnits: true,
			},
		},
		{
			name:  "mixed tienda + comidas",
			types: []string{BusinessTypeTiendaBarrio, BusinessTypeComidasRapidas},
			opts:  CapabilityToggles{},
			want: FeatureFlags{
				EnableTables: true,
				EnableKDS:    true,
				EnableTips:   true,
			},
		},
		{
			name:  "has_tables toggle enables tables even without food",
			types: []string{BusinessTypeTiendaBarrio},
			opts:  CapabilityToggles{Tables: true},
			want: FeatureFlags{
				EnableTables: true,
			},
		},
		{
			name:  "manufactura enables services (B2B)",
			types: []string{BusinessTypeManufactura},
			opts:  CapabilityToggles{},
			want: FeatureFlags{
				EnableServices:      true,
				EnableCustomBilling: true,
			},
		},
		// T-01 (a): toggle servicios → enable_services + enable_custom_billing
		{
			name:  "T-01a: toggle services on tienda_barrio grants services + billing",
			types: []string{BusinessTypeTiendaBarrio},
			opts:  CapabilityToggles{Services: true},
			want: FeatureFlags{
				EnableServices:      true,
				EnableCustomBilling: true,
			},
		},
		// T-01 (b): toggle granel → enable_fractional_units
		{
			name:  "T-01b: toggle fractional units on bar grants fractional",
			types: []string{BusinessTypeBar},
			opts:  CapabilityToggles{FractionalUnits: true},
			want: FeatureFlags{
				EnableTables:          true,
				EnableKDS:             true,
				EnableTips:            true,
				EnableFractionalUnits: true,
			},
		},
		// T-01 (c): toggle mesas → enable_tables but NOT enable_kds / enable_tips
		{
			name:  "T-01c: toggle tables on tienda_barrio grants tables but NOT kds/tips",
			types: []string{BusinessTypeTiendaBarrio},
			opts:  CapabilityToggles{Tables: true},
			want: FeatureFlags{
				EnableTables: true,
				// EnableKDS and EnableTips must remain false
			},
		},
		// T-01 (d): sin toggles → resultado idéntico al comportamiento anterior (AC-07)
		{
			name:  "T-01d: no toggles — tienda_barrio result identical to legacy",
			types: []string{BusinessTypeTiendaBarrio},
			opts:  CapabilityToggles{},
			want:  FeatureFlags{},
		},
		// T-01 (e): tipo OR toggle nunca apaga una capacidad del tipo
		{
			name:  "T-01e: toggles never disable type-implied capabilities (restaurante)",
			types: []string{BusinessTypeRestaurante},
			opts:  CapabilityToggles{Tables: false, Services: false, FractionalUnits: false},
			want: FeatureFlags{
				EnableTables: true,
				EnableKDS:    true,
				EnableTips:   true,
			},
		},
		// All three toggles at once on tienda_barrio
		{
			name:  "all 3 toggles on tienda_barrio",
			types: []string{BusinessTypeTiendaBarrio},
			opts:  CapabilityToggles{Tables: true, Services: true, FractionalUnits: true},
			want: FeatureFlags{
				EnableTables:          true,
				EnableServices:        true,
				EnableCustomBilling:   true,
				EnableFractionalUnits: true,
			},
		},
		// Toggle services on top of manufactura (already has it) — no regression
		{
			name:  "toggle services on manufactura (already has services) — idempotent",
			types: []string{BusinessTypeManufactura},
			opts:  CapabilityToggles{Services: true},
			want: FeatureFlags{
				EnableServices:      true,
				EnableCustomBilling: true,
			},
		},
		// Toggle tables on deposito — no KDS/Tips
		{
			name:  "toggle tables on deposito grants tables+fractional, no kds",
			types: []string{BusinessTypeDepositoConstruccion},
			opts:  CapabilityToggles{Tables: true},
			want: FeatureFlags{
				EnableTables:          true,
				EnableFractionalUnits: true,
			},
		},
		{
			name:  "academias implica eventos (F042)",
			types: []string{BusinessTypeAcademias},
			opts:  CapabilityToggles{},
			want: FeatureFlags{
				EnableEvents: true,
			},
		},
		{
			name:  "tienda no implica eventos",
			types: []string{BusinessTypeTiendaBarrio},
			opts:  CapabilityToggles{},
			want:  FeatureFlags{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultFeatureFlags(tc.types, tc.opts)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidBusinessTypes_ContainsAllConstants(t *testing.T) {
	for _, bt := range []string{
		BusinessTypeTiendaBarrio,
		BusinessTypeMinimercado,
		BusinessTypeDepositoConstruccion,
		BusinessTypeRestaurante,
		BusinessTypeComidasRapidas,
		BusinessTypeBar,
		BusinessTypeManufactura,
		BusinessTypeReparacionMuebles,
		BusinessTypeEmprendimientoGen,
		BusinessTypeAcademias,
	} {
		_, ok := ValidBusinessTypes[bt]
		assert.True(t, ok, "whitelist missing %q", bt)
	}
}
