package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSuggestPrice_BasicMargin(t *testing.T) {
	// $1.150 compra, 20% margen → $1.380 → redondea a $1.400
	result := SuggestPrice(1150, 20)
	assert.Equal(t, float64(1400), result)
}

func TestSuggestPrice_RoundsUp(t *testing.T) {
	// $1.000 compra, 30% → $1.300 (ya redondeado)
	result := SuggestPrice(1000, 30)
	assert.Equal(t, float64(1300), result)
}

func TestSuggestPrice_SmallAmount(t *testing.T) {
	// $500 compra, 25% → $625 → redondea a $650
	result := SuggestPrice(500, 25)
	assert.Equal(t, float64(650), result)
}

func TestSuggestPrice_ZeroMargin(t *testing.T) {
	result := SuggestPrice(1000, 0)
	assert.Equal(t, float64(1000), result)
}

func TestSuggestPrice_LargeAmount(t *testing.T) {
	// $50.000 compra, 15% → $57.500 (exacto)
	result := SuggestPrice(50000, 15)
	assert.Equal(t, float64(57500), result)
}

func TestCalculateProfit(t *testing.T) {
	profit := CalculateProfit(2500, 1800)
	assert.Equal(t, float64(700), profit)
}

func TestCalculateProfit_Zero(t *testing.T) {
	profit := CalculateProfit(1000, 1000)
	assert.Equal(t, float64(0), profit)
}

func TestCalculateMarginPercent(t *testing.T) {
	margin := CalculateMarginPercent(2400, 2000)
	assert.InDelta(t, 20.0, margin, 0.01)
}

func TestCalculateMarginPercent_ZeroPurchase(t *testing.T) {
	margin := CalculateMarginPercent(2500, 0)
	assert.Equal(t, float64(0), margin)
}

func TestRoundCOP_AlreadyRounded(t *testing.T) {
	assert.Equal(t, float64(1500), RoundCOP(1500))
}

func TestRoundCOP_NeedsRounding(t *testing.T) {
	assert.Equal(t, float64(1550), RoundCOP(1520))
}

func TestRoundCOP_ExactFifty(t *testing.T) {
	assert.Equal(t, float64(2050), RoundCOP(2010))
}

func TestRoundCOP_LargeValue(t *testing.T) {
	assert.Equal(t, float64(125050), RoundCOP(125001))
}
