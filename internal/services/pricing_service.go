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

func CalculateMarginPercent(salePrice, purchasePrice float64) float64 {
	if purchasePrice == 0 {
		return 0
	}
	return ((salePrice - purchasePrice) / purchasePrice) * 100
}

func RoundCOP(amount float64) float64 {
	return math.Ceil(amount/50) * 50
}
