// Spec: specs/001-insumos-recetas/spec.md
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// T-01 — RED: the Ingredient model is the new raw-material inventory
// entity. Spec §8 / Plan §3: fields, default Unit, unit enum validation.

func TestIngredient_Fields(t *testing.T) {
	ing := Ingredient{
		TenantID: "11111111-1111-1111-1111-111111111111",
		Name:     "Arroz",
		Unit:     UnitKg,
		Stock:    10,
		MinStock: 2,
		UnitCost: 2900,
	}
	assert.Equal(t, "Arroz", ing.Name)
	assert.Equal(t, UnitKg, ing.Unit)
	assert.Equal(t, float64(10), ing.Stock)
	assert.Equal(t, float64(2), ing.MinStock)
	assert.Equal(t, float64(2900), ing.UnitCost)
}

// AC-05 — an ingredient below its minimum stock is "low stock".
func TestIngredient_IsLowStock(t *testing.T) {
	cases := []struct {
		name     string
		stock    float64
		minStock float64
		want     bool
	}{
		{"below minimum", 1, 2, true},
		{"exactly at minimum", 2, 2, false},
		{"above minimum", 5, 2, false},
		{"zero minimum never low", 0, 0, false},
		{"zero stock with minimum", 0, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ing := Ingredient{Stock: tc.stock, MinStock: tc.minStock}
			assert.Equal(t, tc.want, ing.IsLowStock())
		})
	}
}

// D5 — Unit is a fixed enum {unidad,g,kg,ml,l}.
func TestIsValidUnit(t *testing.T) {
	valid := []string{"unidad", "g", "kg", "ml", "l"}
	for _, u := range valid {
		assert.True(t, IsValidUnit(u), "expected %q to be valid", u)
	}
	invalid := []string{"", "litro", "kilo", "UNIDAD", "lb", "oz"}
	for _, u := range invalid {
		assert.False(t, IsValidUnit(u), "expected %q to be invalid", u)
	}
}

// Default unit when none is provided must be "unidad" (Plan §3).
func TestNormalizeUnit_DefaultsToUnidad(t *testing.T) {
	assert.Equal(t, UnitUnidad, NormalizeUnit(""))
	assert.Equal(t, UnitKg, NormalizeUnit("kg"))
	assert.Equal(t, UnitUnidad, NormalizeUnit("bogus"))
}
