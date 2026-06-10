// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// fxFallbackCOPperUSD is used when the live rate is unavailable (approx).
const fxFallbackCOPperUSD = 4100.0

var (
	fxMu   sync.Mutex
	fxRate float64
	fxAt   time.Time
)

// ExchangeRateUSDCOP — GET /api/v1/fx/usd-cop. Returns how many COP equal 1 USD
// so the client can convert event prices when the organizer switches currency.
// Fetched server-side (avoids browser CORS) and cached ~1h; falls back to an
// approximate rate if the upstream is unreachable.
func ExchangeRateUSDCOP() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"cop_per_usd": copPerUSD()}})
	}
}

func copPerUSD() float64 {
	fxMu.Lock()
	defer fxMu.Unlock()
	if fxRate > 0 && time.Since(fxAt) < time.Hour {
		return fxRate
	}
	if rate := fetchCOPperUSD(); rate > 0 {
		fxRate = rate
		fxAt = time.Now()
		return rate
	}
	if fxRate > 0 {
		return fxRate // tasa vieja, mejor que el fallback fijo
	}
	return fxFallbackCOPperUSD
}

func fetchCOPperUSD() float64 {
	client := http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get("https://open.er-api.com/v6/latest/USD")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var body struct {
		Rates map[string]float64 `json:"rates"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return 0
	}
	return body.Rates["COP"]
}
