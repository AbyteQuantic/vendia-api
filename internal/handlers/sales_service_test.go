package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidateSaleItemRequest covers the XOR invariant between product
// lines and ad-hoc service lines — the same rule migration 020 enforces
// via CHECK. Surfacing this at the app layer returns a Spanish error
// instead of a 500 from the DB.
func TestValidateSaleItemRequest(t *testing.T) {
	t.Parallel()

	realUUID := "11111111-1111-4111-9111-111111111111"

	cases := []struct {
		name    string
		item    SaleItemRequest
		wantErr bool
		errSub  string
	}{
		{
			name: "valid product line",
			item: SaleItemRequest{
				ProductID: realUUID,
				Quantity:  1,
			},
			wantErr: false,
		},
		{
			name: "valid service line",
			item: SaleItemRequest{
				Quantity:          1,
				IsService:         true,
				CustomDescription: "Reparación de mesa",
				CustomUnitPrice:   50000,
			},
			wantErr: false,
		},
		{
			name: "service line cannot carry product_id",
			item: SaleItemRequest{
				ProductID:         realUUID,
				Quantity:          1,
				IsService:         true,
				CustomDescription: "Ilegal",
				CustomUnitPrice:   100,
			},
			wantErr: true,
			errSub:  "product_id",
		},
		{
			name: "service line requires description",
			item: SaleItemRequest{
				Quantity:        1,
				IsService:       true,
				CustomUnitPrice: 100,
			},
			wantErr: true,
			errSub:  "descripción",
		},
		{
			name: "service line requires positive price",
			item: SaleItemRequest{
				Quantity:          1,
				IsService:         true,
				CustomDescription: "Sin precio",
			},
			wantErr: true,
			errSub:  "precio",
		},
		{
			name: "product line requires product_id",
			item: SaleItemRequest{
				Quantity: 1,
			},
			wantErr: true,
			errSub:  "product_id requerido",
		},
		{
			name: "product line rejects non-UUID id",
			item: SaleItemRequest{
				ProductID: "not-a-uuid",
				Quantity:  1,
			},
			wantErr: true,
			errSub:  "UUID",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateSaleItemRequest(tc.item)
			if tc.wantErr {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), tc.errSub)
				}
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestValidateBusinessTypes covers legacy remapping + whitelist enforcement.
// Keeping the function pure lets us assert exact outputs without a DB.
func TestValidateBusinessTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{
			name:  "modern values pass through",
			input: []string{"tienda_barrio", "minimercado"},
			want:  []string{"tienda_barrio", "minimercado"},
		},
		{
			name:  "legacy muebles → reparacion_muebles",
			input: []string{"muebles"},
			want:  []string{"reparacion_muebles"},
		},
		{
			name:  "legacy miscelanea → emprendimiento_general",
			input: []string{"miscelanea"},
			want:  []string{"emprendimiento_general"},
		},
		{
			name:  "legacy reparacion → reparacion_muebles",
			input: []string{"reparacion"},
			want:  []string{"reparacion_muebles"},
		},
		{
			name:  "duplicates after remap are collapsed",
			input: []string{"muebles", "reparacion"},
			want:  []string{"reparacion_muebles"},
		},
		{
			name:    "unknown value rejected",
			input:   []string{"peluqueria"},
			wantErr: true,
		},
		{
			name:  "empty list is valid (caller enforces min=1 elsewhere)",
			input: []string{},
			want:  []string{},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateBusinessTypes(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
