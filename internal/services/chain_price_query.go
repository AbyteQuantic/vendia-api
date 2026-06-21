// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import (
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// ChainPriceMatch — el precio más reciente de un insumo en una cadena, con la
// señal de "bajó de precio" respecto a la base de los últimos 30 días.
type ChainPriceMatch struct {
	Chain         string    `json:"chain"`
	RawName       string    `json:"raw_name"`
	Price         float64   `json:"price"`
	Unit          string    `json:"unit"`
	ScrapedAt     time.Time `json:"scraped_at"`
	Dropped       bool      `json:"dropped"`        // bajó ≥10% vs base 30d
	BaselinePrice float64   `json:"baseline_price"` // promedio 30d (excl. el último)
	DropPct       float64   `json:"drop_pct"`       // % de baja (positivo)
}

const priceDropThreshold = 0.10 // 10%

// MatchChainPrices — para un insumo (nombre ya NORMALIZADO) en una ciudad,
// devuelve el último precio por cadena + si bajó de precio en el mes. Lee del
// histórico append-only (chain_price). Consulta rápida (índices), no scrapea.
func MatchChainPrices(db *gorm.DB, normalizedName, city string) []ChainPriceMatch {
	var rows []models.ChainPrice
	q := db.Where("normalized_name LIKE ?", "%"+normalizedName+"%")
	if city != "" {
		q = q.Where("city = ? OR city = ''", city)
	}
	q.Order("scraped_at DESC").Limit(400).Find(&rows)

	// Agrupa por cadena: el más reciente + base de 30 días.
	type agg struct {
		latest  *models.ChainPrice
		sum30   float64
		count30 int
	}
	byChain := map[string]*agg{}
	cutoff := time.Now().AddDate(0, 0, -30)
	for i := range rows {
		r := rows[i]
		a := byChain[r.Chain]
		if a == nil {
			a = &agg{}
			byChain[r.Chain] = a
		}
		if a.latest == nil || r.ScrapedAt.After(a.latest.ScrapedAt) {
			a.latest = &rows[i]
		}
		if r.ScrapedAt.After(cutoff) {
			a.sum30 += r.Price
			a.count30++
		}
	}

	out := make([]ChainPriceMatch, 0, len(byChain))
	for chain, a := range byChain {
		if a.latest == nil {
			continue
		}
		m := ChainPriceMatch{
			Chain: chain, RawName: a.latest.RawName, Price: a.latest.Price,
			Unit: a.latest.Unit, ScrapedAt: a.latest.ScrapedAt,
		}
		// Base 30d excluyendo el último dato (necesita ≥2 puntos).
		if a.count30 >= 2 {
			base := (a.sum30 - a.latest.Price) / float64(a.count30-1)
			m.BaselinePrice = base
			if base > 0 && a.latest.Price < base*(1-priceDropThreshold) {
				m.Dropped = true
				m.DropPct = (base - a.latest.Price) / base * 100
			}
		}
		out = append(out, m)
	}
	return out
}

// PurgeOldChainPrices borra el histórico con más de 4 meses (se corre en el
// mismo cron tras insertar, para que la tabla no se sature). Devuelve filas
// borradas.
func PurgeOldChainPrices(db *gorm.DB) int64 {
	cutoff := time.Now().AddDate(0, -4, 0)
	res := db.Where("scraped_at < ?", cutoff).Delete(&models.ChainPrice{})
	return res.RowsAffected
}
