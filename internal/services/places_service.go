// Spec: specs/081-mercado-cercano-mapa/spec.md
//
// Ubicaciones de tiendas/mercados cercanos desde OpenStreetMap (Overpass API).
// Gratis, sin llave. Se usa bajo demanda (al abrir "Mercado cercano") → NO
// depende del cron (hoy bloqueado por CRON_TOKEN). Cache server-side por celda
// para respetar los límites de Overpass.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// NearbyMarket es una sede física (supermercado/mercado) cerca del negocio.
type NearbyMarket struct {
	Name    string  `json:"name"`
	Brand   string  `json:"brand,omitempty"`
	Address string  `json:"address,omitempty"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

type placesCacheEntry struct {
	markets []NearbyMarket
	at      time.Time
}

// PlacesService consulta Overpass con un fetcher inyectable (testeable) y
// cachea por celda redondeada (TTL largo: las sedes no cambian a menudo).
type PlacesService struct {
	fetch    func(ctx context.Context, query string) ([]byte, error)
	cacheTTL time.Duration
	mu       sync.Mutex
	cache    map[string]placesCacheEntry
}

func NewPlacesService() *PlacesService {
	return &PlacesService{
		fetch:    overpassFetch,
		cacheTTL: 24 * time.Hour,
		cache:    map[string]placesCacheEntry{},
	}
}

// NewPlacesServiceWithFetch inyecta el fetcher (tests).
func NewPlacesServiceWithFetch(fetch func(ctx context.Context, query string) ([]byte, error)) *PlacesService {
	return &PlacesService{fetch: fetch, cacheTTL: 24 * time.Hour, cache: map[string]placesCacheEntry{}}
}

// NearbyMarkets devuelve supermercados/mercados a ≤ radiusM metros de (lat,lng).
func (s *PlacesService) NearbyMarkets(ctx context.Context, lat, lng float64, radiusM int) ([]NearbyMarket, error) {
	key := placesCacheKey(lat, lng, radiusM)
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Since(e.at) < s.cacheTTL {
		s.mu.Unlock()
		return e.markets, nil
	}
	s.mu.Unlock()

	body, err := s.fetch(ctx, BuildOverpassQuery(lat, lng, radiusM))
	if err != nil {
		return nil, fmt.Errorf("overpass: %w", err)
	}
	markets := ParseOverpassMarkets(body)

	s.mu.Lock()
	s.cache[key] = placesCacheEntry{markets: markets, at: time.Now()}
	s.mu.Unlock()
	return markets, nil
}

// placesCacheKey redondea a ~110 m (3 decimales) + radio → celda de cache.
func placesCacheKey(lat, lng float64, radiusM int) string {
	return fmt.Sprintf("%.3f:%.3f:%d", lat, lng, radiusM)
}

// BuildOverpassQuery arma la consulta Overpass QL para supermercados, tiendas
// de conveniencia y mayoristas en el radio. `out center` da coords también a
// los `way` (polígonos de tienda).
func BuildOverpassQuery(lat, lng float64, radiusM int) string {
	const shops = `^(supermarket|convenience|wholesale|greengrocer)$`
	return fmt.Sprintf(
		`[out:json][timeout:25];(node["shop"~"%[1]s"](around:%[2]d,%[3]f,%[4]f);way["shop"~"%[1]s"](around:%[2]d,%[3]f,%[4]f););out center tags 80;`,
		shops, radiusM, lat, lng,
	)
}

// ParseOverpassMarkets convierte la respuesta JSON de Overpass en sedes. Tolera
// nodos (lat/lon directos) y ways (center.lat/lon). Omite los que no tienen
// nombre ni coords. Pura → unit-testeable.
func ParseOverpassMarkets(body []byte) []NearbyMarket {
	var resp struct {
		Elements []struct {
			Type   string  `json:"type"`
			Lat    float64 `json:"lat"`
			Lon    float64 `json:"lon"`
			Center *struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
			} `json:"center"`
			Tags map[string]string `json:"tags"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	out := make([]NearbyMarket, 0, len(resp.Elements))
	for _, e := range resp.Elements {
		lat, lng := e.Lat, e.Lon
		if lat == 0 && lng == 0 && e.Center != nil {
			lat, lng = e.Center.Lat, e.Center.Lon
		}
		if lat == 0 && lng == 0 {
			continue
		}
		name := e.Tags["name"]
		brand := e.Tags["brand"]
		if name == "" {
			name = brand
		}
		if name == "" {
			continue // sin nombre no sirve al tendero
		}
		out = append(out, NearbyMarket{
			Name:    name,
			Brand:   brand,
			Address: overpassAddress(e.Tags),
			Lat:     lat,
			Lng:     lng,
		})
	}
	return out
}

func overpassAddress(tags map[string]string) string {
	parts := []string{}
	if v := tags["addr:street"]; v != "" {
		if n := tags["addr:housenumber"]; n != "" {
			parts = append(parts, v+" "+n)
		} else {
			parts = append(parts, v)
		}
	}
	if v := tags["addr:suburb"]; v != "" {
		parts = append(parts, v)
	}
	if v := tags["addr:city"]; v != "" {
		parts = append(parts, v)
	}
	return strings.Join(parts, ", ")
}

// overpassFetch hace el POST real a Overpass con un User-Agent claro (política
// de uso de OSM). Round-robin simple de mirrors no — un endpoint estable.
func overpassFetch(ctx context.Context, query string) ([]byte, error) {
	endpoint := "https://overpass-api.de/api/interpreter"
	form := url.Values{"data": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "VendIA/1.0 (mercado-cercano; soporte@vendia.store)")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MB tope
}

// haversineKmSvc — distancia km (duplicado mínimo del de handlers para no crear
// dependencia services→handlers). 6371 km radio terrestre.
func haversineKmSvc(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// SortMarketsByDistance ordena (y filtra > radio) las sedes por cercanía a
// (lat,lng). Devuelve pares {market, distKm}. Pura → testeable.
type MarketWithDistance struct {
	Market   NearbyMarket
	DistKm   float64
}

func SortMarketsByDistance(markets []NearbyMarket, lat, lng, radiusKm float64) []MarketWithDistance {
	out := make([]MarketWithDistance, 0, len(markets))
	for _, m := range markets {
		d := haversineKmSvc(lat, lng, m.Lat, m.Lng)
		if d <= radiusKm {
			out = append(out, MarketWithDistance{Market: m, DistKm: d})
		}
	}
	// orden por distancia asc (insertion simple; listas chicas).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].DistKm < out[j-1].DistKm; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
