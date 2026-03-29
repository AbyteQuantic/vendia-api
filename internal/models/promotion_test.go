package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPromotion_Discount(t *testing.T) {
	promo := Promotion{
		ProductName: "Yogur Alpina",
		OrigPrice:   2500,
		PromoPrice:  1800,
		PromoType:   "discount",
		IsActive:    true,
	}

	discount := promo.OrigPrice - promo.PromoPrice
	assert.Equal(t, float64(700), discount)

	discountPct := (discount / promo.OrigPrice) * 100
	assert.InDelta(t, 28.0, discountPct, 0.1)
}

func TestPromotion_DefaultType(t *testing.T) {
	promo := Promotion{}
	assert.Equal(t, false, promo.IsActive) // zero value
}
