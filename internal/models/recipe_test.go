package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecipe_CostCalculation(t *testing.T) {
	recipe := Recipe{
		ProductName: "Perro Caliente",
		SalePrice:   5000,
		Ingredients: []RecipeIngredient{
			{ProductName: "Pan", Quantity: 1, UnitCost: 500},
			{ProductName: "Salchicha", Quantity: 1, UnitCost: 800},
			{ProductName: "Salsa", Quantity: 0.5, UnitCost: 200},
		},
	}

	var totalCost float64
	for _, ing := range recipe.Ingredients {
		totalCost += ing.Quantity * ing.UnitCost
	}

	assert.Equal(t, float64(1400), totalCost)
	assert.Equal(t, float64(3600), recipe.SalePrice-totalCost)
}

func TestRecipeIngredient_Fields(t *testing.T) {
	ing := RecipeIngredient{
		ProductUUID: "prod-uuid",
		ProductName: "Pan para perro",
		Quantity:    12,
		UnitCost:    500,
		Emoji:       "🍞",
	}
	assert.Equal(t, float64(6000), ing.Quantity*ing.UnitCost)
}
