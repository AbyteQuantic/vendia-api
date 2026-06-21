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

// comparable — el precio para comparar: por unidad base si hay paquete, si no el
// precio crudo (evita meter peras con manzanas).
func comparable(r models.ChainPrice) float64 {
	if r.PricePerBaseUnit > 0 {
		return r.PricePerBaseUnit
	}
	return r.Price
}

// dayKey trunca a día (para agrupar el histórico por fecha de scraping).
func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// MatchChainPrices — para un insumo (nombre NORMALIZADO) en una ciudad, devuelve
// el MEJOR precio representativo por cadena (el más barato por unidad base, lo
// que evita bultos/atípicos) y si BAJÓ de precio respecto a fechas anteriores.
// Lee del histórico append-only; consulta rápida, no scrapea.
func MatchChainPrices(db *gorm.DB, normalizedName, city string) []ChainPriceMatch {
	var rows []models.ChainPrice
	q := db.Where("normalized_name LIKE ?", "%"+normalizedName+"%")
	if city != "" {
		q = q.Where("city = ? OR city = ''", city)
	}
	q.Order("scraped_at DESC").Limit(800).Find(&rows)

	byChain := map[string][]models.ChainPrice{}
	for _, r := range rows {
		if !IsFoodCategory(r.Category) {
			continue // defensa: ignora ruido no-comestible aún almacenado
		}
		byChain[r.Chain] = append(byChain[r.Chain], r)
	}

	out := make([]ChainPriceMatch, 0, len(byChain))
	for chain, list := range byChain {
		// Mejor representante (más barato por unidad base) en la fecha más reciente.
		latestDay := ""
		for _, r := range list {
			if k := dayKey(r.ScrapedAt); k > latestDay {
				latestDay = k
			}
		}
		var best *models.ChainPrice
		for i := range list {
			if dayKey(list[i].ScrapedAt) != latestDay {
				continue
			}
			if best == nil || comparable(list[i]) < comparable(*best) {
				best = &list[i]
			}
		}
		if best == nil {
			continue
		}

		// Base: el mejor representante en fechas ANTERIORES (≥1 día previo).
		var prevBest *models.ChainPrice
		for i := range list {
			if dayKey(list[i].ScrapedAt) >= latestDay {
				continue
			}
			if prevBest == nil || comparable(list[i]) < comparable(*prevBest) {
				prevBest = &list[i]
			}
		}

		m := ChainPriceMatch{
			Chain: chain, RawName: best.RawName, Price: best.Price,
			Unit: best.Unit, ScrapedAt: best.ScrapedAt,
		}
		if prevBest != nil {
			base := comparable(*prevBest)
			now := comparable(*best)
			m.BaselinePrice = prevBest.Price
			if base > 0 && now < base*(1-priceDropThreshold) {
				m.Dropped = true
				m.DropPct = (base - now) / base * 100
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
