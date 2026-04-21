package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultFeatureFlags(t *testing.T) {
	cases := []struct {
		name      string
		types     []string
		hasTables bool
		want      FeatureFlags
	}{
		{
			name:      "tienda_barrio — nothing enabled",
			types:     []string{BusinessTypeTiendaBarrio},
			hasTables: false,
			want:      FeatureFlags{},
		},
		{
			name:      "restaurante enables food stack",
			types:     []string{BusinessTypeRestaurante},
			hasTables: false,
			want: FeatureFlags{
				EnableTables: true,
				EnableKDS:    true,
				EnableTips:   true,
			},
		},
		{
			name:      "bar + restaurante still deduplicates flags",
			types:     []string{BusinessTypeBar, BusinessTypeRestaurante},
			hasTables: false,
			want: FeatureFlags{
				EnableTables: true,
				EnableKDS:    true,
				EnableTips:   true,
			},
		},
		{
			name:      "reparacion_muebles enables services + custom billing",
			types:     []string{BusinessTypeReparacionMuebles},
			hasTables: false,
			want: FeatureFlags{
				EnableServices:      true,
				EnableCustomBilling: true,
			},
		},
		{
			name:      "deposito_construccion enables fractional units",
			types:     []string{BusinessTypeDepositoConstruccion},
			hasTables: false,
			want: FeatureFlags{
				EnableFractionalUnits: true,
			},
		},
		{
			name:      "mixed tienda + comidas",
			types:     []string{BusinessTypeTiendaBarrio, BusinessTypeComidasRapidas},
			hasTables: false,
			want: FeatureFlags{
				EnableTables: true,
				EnableKDS:    true,
				EnableTips:   true,
			},
		},
		{
			name:      "has_tables override enables tables even without food",
			types:     []string{BusinessTypeTiendaBarrio},
			hasTables: true,
			want: FeatureFlags{
				EnableTables: true,
			},
		},
		{
			name:      "manufactura enables services (B2B)",
			types:     []string{BusinessTypeManufactura},
			hasTables: false,
			want: FeatureFlags{
				EnableServices:      true,
				EnableCustomBilling: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultFeatureFlags(tc.types, tc.hasTables)
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
	} {
		_, ok := ValidBusinessTypes[bt]
		assert.True(t, ok, "whitelist missing %q", bt)
	}
}
