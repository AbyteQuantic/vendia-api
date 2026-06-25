// Spec: specs/081-mercado-cercano-mapa/spec.md
//
// Ubicaciones de tiendas/mercados cercanos para el MAPA "Mercado cercano".
// Multi-fuente, bajo demanda (no depende del cron), con cache por celda:
//   1. OpenStreetMap (Overpass) — gratis, cualquier supermercado mapeado.
//   2. VTEX pickup-points de las cadenas del scraper (Éxito, Olímpica) —
//      gratis, da sedes REALES con coords aunque OSM no las tenga.
//   3. Google Places — OPCIONAL (solo si GOOGLE_PLACES_API_KEY está set):
//      cubre cadenas que faltan (D1, Ara). Con costo → opt-in.
// Se fusionan y deduplican por (nombre normalizado + coord redondeada).
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
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
	Source  string  `json:"source,omitempty"` // osm | exito | olimpica | google
}

type vtexChain struct{ Chain, BaseURL string }

type placesCacheEntry struct {
	markets []NearbyMarket
	at      time.Time
}

// PlacesService agrega las fuentes. Los fetchers son inyectables (tests).
type PlacesService struct {
	overpass   func(ctx context.Context, query string) ([]byte, error)
	httpGet    func(ctx context.Context, rawURL string) ([]byte, error)
	vtexChains []vtexChain
	googleKey  string
	cacheTTL   time.Duration
	mu         sync.Mutex
	cache      map[string]placesCacheEntry
}

func NewPlacesService() *PlacesService {
	return &PlacesService{
		overpass: overpassFetch,
		httpGet:  defaultHTTPGet,
		vtexChains: []vtexChain{
			{Chain: "exito", BaseURL: "https://www.exito.com"},
			{Chain: "olimpica", BaseURL: "https://www.olimpica.com"},
		},
		googleKey: os.Getenv("GOOGLE_PLACES_API_KEY"),
		cacheTTL:  24 * time.Hour,
		cache:     map[string]placesCacheEntry{},
	}
}

// NewPlacesServiceWithFetch inyecta ambos fetchers + cadenas + llave (tests).
func NewPlacesServiceWithFetch(
	overpass func(ctx context.Context, query string) ([]byte, error),
	httpGet func(ctx context.Context, rawURL string) ([]byte, error),
	chains []vtexChain,
	googleKey string,
) *PlacesService {
	return &PlacesService{
		overpass:   overpass,
		httpGet:    httpGet,
		vtexChains: chains,
		googleKey:  googleKey,
		cacheTTL:   24 * time.Hour,
		cache:      map[string]placesCacheEntry{},
	}
}

// NearbyMarkets fusiona todas las fuentes para (lat,lng) en un radio. Cachea el
// resultado combinado por celda. Cada fuente falla en silencio: una caída de
// Overpass no debe tumbar las sedes VTEX, ni viceversa.
func (s *PlacesService) NearbyMarkets(ctx context.Context, lat, lng float64, radiusM int) ([]NearbyMarket, error) {
	key := placesCacheKey(lat, lng, radiusM)
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Since(e.at) < s.cacheTTL {
		s.mu.Unlock()
		return e.markets, nil
	}
	s.mu.Unlock()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		all  []NearbyMarket
		errs int
	)
	add := func(ms []NearbyMarket, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs++
			return
		}
		all = append(all, ms...)
	}

	// 1. OSM — Overpass suele ser LENTO; cap a 10s para no bloquear la respuesta
	// (si tarda más, VTEX igual responde y el mapa muestra esas sedes).
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		body, err := s.overpass(c, BuildOverpassQuery(lat, lng, radiusM))
		if err != nil {
			add(nil, err)
			return
		}
		add(ParseOverpassMarkets(body), nil)
	}()

	// 2. VTEX pickup-points por cadena (rápido; cap 8s por si una cadena cuelga).
	for _, ch := range s.vtexChains {
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			u := fmt.Sprintf("%s/api/checkout/pub/pickup-points?geoCoordinates=%f;%f", ch.BaseURL, lng, lat)
			body, err := s.httpGet(c, u)
			if err != nil {
				add(nil, err)
				return
			}
			add(ParseVTEXPickupPoints(body, ch.Chain), nil)
		}()
	}

	// 3. Google Places (opcional).
	if s.googleKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := fmt.Sprintf(
				"https://maps.googleapis.com/maps/api/place/nearbysearch/json?location=%f,%f&radius=%d&type=supermarket&key=%s",
				lat, lng, radiusM, url.QueryEscape(s.googleKey))
			body, err := s.httpGet(ctx, u)
			if err != nil {
				add(nil, err)
				return
			}
			add(ParseGooglePlaces(body), nil)
		}()
	}

	wg.Wait()

	merged := DedupMarkets(all)
	// Si TODAS las fuentes fallaron y no hay nada → error; si al menos una dio
	// datos (o vacío legítimo), devolvemos lo que haya.
	if len(merged) == 0 && errs > 0 && errs >= s.sourceCount() {
		return nil, fmt.Errorf("todas las fuentes de mapa fallaron")
	}

	s.mu.Lock()
	s.cache[key] = placesCacheEntry{markets: merged, at: time.Now()}
	s.mu.Unlock()
	return merged, nil
}

func (s *PlacesService) sourceCount() int {
	n := 1 + len(s.vtexChains) // OSM + VTEX
	if s.googleKey != "" {
		n++
	}
	return n
}

func placesCacheKey(lat, lng float64, radiusM int) string {
	return fmt.Sprintf("%.3f:%.3f:%d", lat, lng, radiusM)
}

// DedupMarkets quita repetidos por (nombre normalizado + coord ~110m). Cuando
// dos fuentes traen la misma sede, gana la primera (orden: OSM, VTEX, Google).
func DedupMarkets(in []NearbyMarket) []NearbyMarket {
	seen := map[string]bool{}
	out := make([]NearbyMarket, 0, len(in))
	for _, m := range in {
		k := fmt.Sprintf("%.3f:%.3f:%s", m.Lat, m.Lng,
			strings.ToLower(strings.TrimSpace(m.Name)))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	return out
}

// ── OSM / Overpass ────────────────────────────────────────────────────────

func BuildOverpassQuery(lat, lng float64, radiusM int) string {
	const shops = `^(supermarket|convenience|wholesale|greengrocer)$`
	return fmt.Sprintf(
		`[out:json][timeout:25];(node["shop"~"%[1]s"](around:%[2]d,%[3]f,%[4]f);way["shop"~"%[1]s"](around:%[2]d,%[3]f,%[4]f););out center tags 80;`,
		shops, radiusM, lat, lng,
	)
}

func ParseOverpassMarkets(body []byte) []NearbyMarket {
	var resp struct {
		Elements []struct {
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
			continue
		}
		out = append(out, NearbyMarket{
			Name: name, Brand: brand, Address: overpassAddress(e.Tags),
			Lat: lat, Lng: lng, Source: "osm",
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

// ── VTEX pickup-points ─────────────────────────────────────────────────────

// ParseVTEXPickupPoints convierte la respuesta de
// {baseURL}/api/checkout/pub/pickup-points en sedes. OJO: VTEX da las coords
// como [lng, lat] (al revés). Pura → testeable.
func ParseVTEXPickupPoints(body []byte, chain string) []NearbyMarket {
	var resp struct {
		Items []struct {
			PickupPoint struct {
				FriendlyName string `json:"friendlyName"`
				Address      struct {
					GeoCoordinates []float64 `json:"geoCoordinates"` // [lng, lat]
					Street         string    `json:"street"`
					Number         string    `json:"number"`
					City           string    `json:"city"`
					Neighborhood   string    `json:"neighborhood"`
				} `json:"address"`
			} `json:"pickupPoint"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	brand := vtexBrandLabel(chain)
	out := make([]NearbyMarket, 0, len(resp.Items))
	for _, it := range resp.Items {
		pp := it.PickupPoint
		if len(pp.Address.GeoCoordinates) < 2 {
			continue
		}
		lng, lat := pp.Address.GeoCoordinates[0], pp.Address.GeoCoordinates[1]
		if lat == 0 && lng == 0 {
			continue
		}
		name := strings.TrimSpace(pp.FriendlyName)
		if name == "" {
			name = brand
		}
		addrParts := []string{}
		if pp.Address.Street != "" {
			addrParts = append(addrParts, strings.TrimSpace(pp.Address.Street+" "+pp.Address.Number))
		}
		if pp.Address.Neighborhood != "" {
			addrParts = append(addrParts, pp.Address.Neighborhood)
		}
		if pp.Address.City != "" {
			addrParts = append(addrParts, pp.Address.City)
		}
		out = append(out, NearbyMarket{
			Name: name, Brand: brand, Address: strings.Join(addrParts, ", "),
			Lat: lat, Lng: lng, Source: chain,
		})
	}
	return out
}

func vtexBrandLabel(chain string) string {
	switch chain {
	case "exito":
		return "Éxito"
	case "olimpica":
		return "Olímpica"
	default:
		return strings.Title(chain) //nolint:staticcheck — etiqueta simple
	}
}

// ── Google Places (opcional) ───────────────────────────────────────────────

func ParseGooglePlaces(body []byte) []NearbyMarket {
	var resp struct {
		Results []struct {
			Name     string `json:"name"`
			Vicinity string `json:"vicinity"`
			Geometry struct {
				Location struct {
					Lat float64 `json:"lat"`
					Lng float64 `json:"lng"`
				} `json:"location"`
			} `json:"geometry"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	out := make([]NearbyMarket, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Name == "" || (r.Geometry.Location.Lat == 0 && r.Geometry.Location.Lng == 0) {
			continue
		}
		out = append(out, NearbyMarket{
			Name: r.Name, Address: r.Vicinity,
			Lat: r.Geometry.Location.Lat, Lng: r.Geometry.Location.Lng,
			Source: "google",
		})
	}
	return out
}

// ── HTTP fetchers reales ───────────────────────────────────────────────────

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
	return doHTTP(req)
}

func defaultHTTPGet(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (VendIA mercado-cercano)")
	return doHTTP(req)
}

func doHTTP(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// ── Distancia / orden ──────────────────────────────────────────────────────

func haversineKmSvc(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

type MarketWithDistance struct {
	Market NearbyMarket
	DistKm float64
}

func SortMarketsByDistance(markets []NearbyMarket, lat, lng, radiusKm float64) []MarketWithDistance {
	out := make([]MarketWithDistance, 0, len(markets))
	for _, m := range markets {
		d := haversineKmSvc(lat, lng, m.Lat, m.Lng)
		if d <= radiusKm {
			out = append(out, MarketWithDistance{Market: m, DistKm: d})
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].DistKm < out[j-1].DistKm; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
