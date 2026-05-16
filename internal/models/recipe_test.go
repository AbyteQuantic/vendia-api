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
	insumoID := "c1000000-0000-4000-8000-000000000099"
	ing := RecipeIngredient{
		// Feature 001 insumo contract: a recipe line points at an
		// Ingredient via IngredientID; ProductName/UnitCost are the
		// server-side snapshot of that insumo.
		IngredientID: &insumoID,
		ProductName:  "Pan para perro",
		Quantity:     12,
		UnitCost:     500,
		Emoji:        "🍞",
	}
	assert.Equal(t, float64(6000), ing.Quantity*ing.UnitCost)
	if assert.NotNil(t, ing.IngredientID) {
		assert.Equal(t, insumoID, *ing.IngredientID)
	}
	assert.Nil(t, ing.ProductUUID, "new insumo-oriented lines leave ProductUUID nil")
}

// A legacy product-oriented line can still carry a ProductUUID — the
// field is a nullable *string so old data keeps working (Art. X).
func TestRecipeIngredient_LegacyProductUUID(t *testing.T) {
	prodUUID := "p1000000-0000-4000-8000-000000000099"
	ing := RecipeIngredient{
		ProductUUID: &prodUUID,
		ProductName: "Producto legado",
		Quantity:    1,
		UnitCost:    300,
	}
	if assert.NotNil(t, ing.ProductUUID) {
		assert.Equal(t, prodUUID, *ing.ProductUUID)
	}
	assert.Nil(t, ing.IngredientID)
}
