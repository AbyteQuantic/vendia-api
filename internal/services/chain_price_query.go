// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import (
	"sort"
	"strings"
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

	// Datos del empaque para el costo empaque-completo + recomendado (Spec 077).
	PackQty          float64 `json:"pack_qty"`
	PricePerBaseUnit float64 `json:"price_per_base_unit"`
	Brand            string  `json:"brand,omitempty"`
}

const priceDropThreshold = 0.10 // 10%

// comparable — el precio para comparar la MISMA presentación (bajó-de-precio):
// por unidad base si hay paquete, si no el precio crudo.
func comparable(r models.ChainPrice) float64 {
	if r.PricePerBaseUnit > 0 {
		return r.PricePerBaseUnit
	}
	return r.Price
}

// processedSignals — palabras que delatan un DERIVADO/procesado, no el insumo
// crudo (ej "aceite de aguacate", "pulpa de aguacate", "vinagreta con aguacate").
var processedSignals = []string{
	"aceite", "pulpa", "salsa", "vinagreta", "esencia", "extracto", "saborizado",
	"sabor a", "aroma", "snack", "chips", "dip", "untable", "mermelada", "gelatina",
	"galleta", "cereal", "yogur", "helado", "bebida", "polvo", "concentrado",
}

// relevanceScore puntúa qué tan bien un producto REPRESENTA al insumo buscado:
// el insumo crudo (nombre que empieza por el insumo, sin derivados) gana al
// derivado y al homónimo. Evita que "aceite/pulpa de aguacate" o un mordedor le
// ganen al aguacate de verdad por ser "más baratos por gramo" (Spec 077).
func relevanceScore(productNorm, ingredientNorm string) int {
	if ingredientNorm == "" || !strings.Contains(productNorm, ingredientNorm) {
		return -100
	}
	score := 0
	if strings.HasPrefix(productNorm, ingredientNorm) {
		score += 12 // el insumo es el sustantivo principal
	}
	score -= strings.Index(productNorm, ingredientNorm) / 4 // antes = mejor
	for _, sig := range processedSignals {
		if strings.Contains(productNorm, sig) && !strings.Contains(ingredientNorm, sig) {
			score -= 8
		}
	}
	if w := len(strings.Fields(productNorm)); w > 6 {
		score -= (w - 6) // nombres larguísimos suelen ser compuestos
	}
	return score
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
		if !isFoodProduct(r.RawName, r.Category) {
			continue // defensa: ignora ruido no-comestible (categoría Y nombre)
		}
		byChain[r.Chain] = append(byChain[r.Chain], r)
	}

	out := make([]ChainPriceMatch, 0, len(byChain))
	for chain, list := range byChain {
		// Mejor representante en la fecha más reciente: PRIMERO el más RELEVANTE
		// (el insumo crudo, no un derivado/homónimo), y a igual relevancia el más
		// barato. Esto evita que un mordedor o un "aceite de aguacate" gane por
		// tener precio-por-gramo chico (Spec 077, fix "sugerencia equivocada").
		latestDay := ""
		for _, r := range list {
			if k := dayKey(r.ScrapedAt); k > latestDay {
				latestDay = k
			}
		}
		today := make([]models.ChainPrice, 0, len(list))
		for i := range list {
			if dayKey(list[i].ScrapedAt) == latestDay {
				today = append(today, list[i])
			}
		}
		sort.SliceStable(today, func(a, b int) bool {
			ra := relevanceScore(today[a].NormalizedName, normalizedName)
			rb := relevanceScore(today[b].NormalizedName, normalizedName)
			if ra != rb {
				return ra > rb // más relevante primero
			}
			return today[a].Price < today[b].Price // luego el más barato
		})
		if len(today) == 0 {
			continue
		}
		best := &today[0]

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
			PackQty: best.PackQty, PricePerBaseUnit: best.PricePerBaseUnit, Brand: best.Brand,
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

// BestChainPrice devuelve la mejor sugerencia de cadena para un insumo (la más
// relevante y barata entre cadenas), o nil si no hay match. Es el fallback de
// precio cuando el tenant NO tiene compra previa ni precio de proveedor: en vez
// de "sin precio", sugiere lo que cuesta en las cadenas (Spec 077, fix #1).
func BestChainPrice(db *gorm.DB, normalizedName, city string) *ChainPriceMatch {
	matches := MatchChainPrices(db, normalizedName, city)
	if len(matches) == 0 {
		return nil
	}
	best := &matches[0]
	for i := range matches {
		if matches[i].Price < best.Price { // entrada más baja para el tendero
			best = &matches[i]
		}
	}
	return best
}

// PurgeOldChainPrices borra el histórico con más de 4 meses (se corre en el
// mismo cron tras insertar, para que la tabla no se sature). Devuelve filas
// borradas.
func PurgeOldChainPrices(db *gorm.DB) int64 {
	cutoff := time.Now().AddDate(0, -4, 0)
	res := db.Where("scraped_at < ?", cutoff).Delete(&models.ChainPrice{})
	return res.RowsAffected
}
