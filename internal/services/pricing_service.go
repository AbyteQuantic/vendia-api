package services

import "math"

func SuggestPrice(purchasePrice float64, marginPercent float64) float64 {
	suggested := purchasePrice * (1 + marginPercent/100)
	rounded := math.Ceil(suggested/50) * 50
	return rounded
}

func CalculateProfit(salePrice, purchasePrice float64) float64 {
	return salePrice - purchasePrice
}

// CalculateMarginPercent returns the gross margin as a percentage of
// the sale price: (precio − costo) / precio * 100. This is the figure
// the tendero understands as "margen" — what fraction of each peso
// charged is profit. (The previous formula divided by the cost, which
// yields markup, not margin — FR-04.)
func CalculateMarginPercent(salePrice, purchasePrice float64) float64 {
	if salePrice == 0 {
		return 0
	}
	return ((salePrice - purchasePrice) / salePrice) * 100
}

func RoundCOP(amount float64) float64 {
	return math.Ceil(amount/50) * 50
}
