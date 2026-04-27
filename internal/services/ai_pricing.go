package services

import "strings"

// USD per 1M tokens (Google AI Studio / Vertex-style list pricing, Apr 2026).
// "Flash" tier covers 2.0/2.5 Flash and image-flash; "Pro" for 1.5 Pro, etc.
// Tuned for FinOps order-of-magnitude; adjust as Google publishes new rates.
const (
	GeminiFlashInputPer1MUSD  = 0.075
	GeminiFlashOutputPer1MUSD = 0.30
	GeminiProInputPer1MUSD     = 1.25
	GeminiProOutputPer1MUSD    = 5.00
)

// EstimateGeminiCostUSD returns estimated cost from token counts and model name.
// Image-oriented models (substrings "image" / "imagen") use output-heavy Flash
// rates for both directions as a stable fallback when the API blurs in/out.
func EstimateGeminiCostUSD(modelName string, inputTokens, outputTokens int) float64 {
	m := strings.ToLower(modelName)
	flash := true
	if strings.Contains(m, "pro") && !strings.Contains(m, "flash") {
		flash = false
	}
	in := float64(inputTokens) / 1e6
	out := float64(outputTokens) / 1e6
	if flash {
		return in*GeminiFlashInputPer1MUSD + out*GeminiFlashOutputPer1MUSD
	}
	return in*GeminiProInputPer1MUSD + out*GeminiProOutputPer1MUSD
}
