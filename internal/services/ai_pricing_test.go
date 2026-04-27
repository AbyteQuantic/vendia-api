package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEstimateGeminiCostUSD_FlashTier(t *testing.T) {
	c := EstimateGeminiCostUSD("gemini-2.0-flash", 1_000_000, 1_000_000)
	// 0.075 + 0.30 = 0.375
	assert.InDelta(t, 0.375, c, 1e-9)
}

func TestEstimateGeminiCostUSD_ProTier(t *testing.T) {
	c := EstimateGeminiCostUSD("gemini-1.5-pro", 1_000_000, 1_000_000)
	assert.InDelta(t, 1.25+5.00, c, 1e-9)
}
