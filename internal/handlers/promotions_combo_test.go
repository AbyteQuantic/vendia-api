package handlers

import (
	"testing"
	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
)

// TestCalculatePromoFinancials covers the pure combo math used by the
// Flutter PromoBuilder's live calculator. Each case is a realistic
// shopkeeper scenario so that when prices or margins change, the test
// failure message points directly at the business rule that broke.
func TestCalculatePromoFinancials(t *testing.T) {
	t.Parallel()

	// Reusable stock for the lookup map — purchase price vs shelf price
	// reflects typical Colombian corner-store margins (~25–30%).
	mk := func(id string, purchase, price float64) models.Product {
		return models.Product{BaseModel: models.BaseModel{ID: id}, PurchasePrice: purchase, Price: price}
	}
	coke := mk("p-coke", 1800, 2500)
	chips := mk("p-chips", 1100, 1800)
	candy := mk("p-candy", 400, 900)
	freeItem := mk("p-free", 500, 1000)

	lookup := map[string]models.Product{
		coke.ID: coke, chips.ID: chips, candy.ID: candy, freeItem.ID: freeItem,
	}

	cases := []struct {
		name           string
		items          []models.PromotionItem
		wantCost       float64
		wantRegular    float64
		wantPromo      float64
		wantDiscount   float64
		wantPercent    float64
		wantProfit     float64
		wantProfitable bool
	}{
		{
			name: "2x1 in sodas — still profitable",
			items: []models.PromotionItem{
				{ProductID: coke.ID, Quantity: 2, PromoPrice: 2500}, // pay 2, get 2 → 2500 total
			},
			wantCost: 3600, wantRegular: 5000, wantPromo: 5000,
			wantDiscount: 0, wantPercent: 0,
			wantProfit: 1400, wantProfitable: true,
		},
		{
			name: "combo chips + candy at 20% off",
			items: []models.PromotionItem{
				{ProductID: chips.ID, Quantity: 1, PromoPrice: 1500}, // 1800 → 1500
				{ProductID: candy.ID, Quantity: 1, PromoPrice: 700},  // 900 → 700
			},
			wantCost: 1500, wantRegular: 2700, wantPromo: 2200,
			wantDiscount: 500, wantPercent: 18.52,
			wantProfit: 700, wantProfitable: true,
		},
		{
			name: "loss leader — below cost, not profitable",
			items: []models.PromotionItem{
				{ProductID: freeItem.ID, Quantity: 1, PromoPrice: 300},
			},
			wantCost: 500, wantRegular: 1000, wantPromo: 300,
			wantDiscount: 700, wantPercent: 70,
			wantProfit: -200, wantProfitable: false,
		},
		{
			name:           "empty combo yields zeros and flagged profitable (no loss)",
			items:          nil,
			wantProfitable: true,
		},
		{
			name: "missing product in lookup is skipped silently",
			items: []models.PromotionItem{
				{ProductID: "p-unknown", Quantity: 3, PromoPrice: 999},
				{ProductID: coke.ID, Quantity: 1, PromoPrice: 2000},
			},
			wantCost: 1800, wantRegular: 2500, wantPromo: 2000,
			wantDiscount: 500, wantPercent: 20,
			wantProfit: 200, wantProfitable: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := calculatePromoFinancials(tc.items, lookup)
			assert.InDelta(t, tc.wantCost, f.TotalCost, 0.01, "TotalCost")
			assert.InDelta(t, tc.wantRegular, f.TotalRegular, 0.01, "TotalRegular")
			assert.InDelta(t, tc.wantPromo, f.TotalPromo, 0.01, "TotalPromo")
			assert.InDelta(t, tc.wantDiscount, f.DiscountAmount, 0.01, "DiscountAmount")
			assert.InDelta(t, tc.wantPercent, f.DiscountPercent, 0.01, "DiscountPercent")
			assert.InDelta(t, tc.wantProfit, f.NetProfit, 0.01, "NetProfit")
			assert.Equal(t, tc.wantProfitable, f.IsProfitable, "IsProfitable")
		})
	}
}
