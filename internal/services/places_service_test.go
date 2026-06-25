// Spec: specs/081-mercado-cercano-mapa/spec.md
package services

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildOverpassQuery_IncludesShopsAndRadius(t *testing.T) {
	q := BuildOverpassQuery(4.36, -74.80, 5000)
	assert.Contains(t, q, "supermarket")
	assert.Contains(t, q, "convenience")
	assert.Contains(t, q, "around:5000")
	assert.Contains(t, q, "out center tags")
}

func TestParseOverpassMarkets_NodesAndWays(t *testing.T) {
	body := []byte(`{"elements":[
	  {"type":"node","lat":4.361,"lon":-74.808,"tags":{"shop":"supermarket","name":"Supercundi","addr:street":"Cra 7","addr:housenumber":"10-20","addr:city":"Girardot"}},
	  {"type":"way","center":{"lat":4.362,"lon":-74.809},"tags":{"shop":"supermarket","name":"Mercado X"}},
	  {"type":"node","lat":4.363,"lon":-74.81,"tags":{"shop":"convenience"}},
	  {"type":"node","lat":0,"lon":0,"tags":{"shop":"supermarket","name":"Sin coords"}}
	]}`)
	markets := ParseOverpassMarkets(body)
	require.Len(t, markets, 2)
	assert.Equal(t, "Supercundi", markets[0].Name)
	assert.Equal(t, "Cra 7 10-20, Girardot", markets[0].Address)
	assert.Equal(t, "osm", markets[0].Source)
	assert.InDelta(t, 4.362, markets[1].Lat, 1e-9)
}

func TestParseVTEXPickupPoints_RealShape(t *testing.T) {
	// VTEX da coords [lng, lat] (al revés) — el parser debe corregirlo.
	body := []byte(`{"items":[
	  {"distance":6.1,"pickupPoint":{"friendlyName":"Exito Girardot","address":{"geoCoordinates":[-74.80409,4.302444],"street":"Carrera 7A","number":"33-77","city":"Girardot","neighborhood":"Centro"}}},
	  {"distance":9,"pickupPoint":{"friendlyName":"Punto Entrega Anapoima","address":{"geoCoordinates":[-74.80721,4.287443],"city":"Anapoima"}}},
	  {"pickupPoint":{"friendlyName":"Sin coords","address":{"geoCoordinates":[]}}}
	]}`)
	markets := ParseVTEXPickupPoints(body, "exito")
	require.Len(t, markets, 2, "omite el sin-coords")
	assert.Equal(t, "Exito Girardot", markets[0].Name)
	assert.Equal(t, "Éxito", markets[0].Brand)
	assert.Equal(t, "exito", markets[0].Source)
	assert.InDelta(t, 4.302444, markets[0].Lat, 1e-9, "lat correcto desde [lng,lat]")
	assert.InDelta(t, -74.80409, markets[0].Lng, 1e-9)
	assert.Contains(t, markets[0].Address, "Girardot")
}

func TestParseGooglePlaces_Shape(t *testing.T) {
	body := []byte(`{"results":[
	  {"name":"D1","vicinity":"Cra 10 #5","geometry":{"location":{"lat":4.30,"lng":-74.80}}},
	  {"name":"Ara","vicinity":"Calle 8","geometry":{"location":{"lat":4.31,"lng":-74.81}}},
	  {"name":"","geometry":{"location":{"lat":4.32,"lng":-74.82}}}
	]}`)
	m := ParseGooglePlaces(body)
	require.Len(t, m, 2, "omite el sin-nombre")
	assert.Equal(t, "D1", m[0].Name)
	assert.Equal(t, "google", m[0].Source)
}

func TestDedupMarkets_RemovesSameNameAndCoord(t *testing.T) {
	in := []NearbyMarket{
		{Name: "Éxito Girardot", Lat: 4.3024, Lng: -74.8041, Source: "osm"},
		{Name: "éxito girardot", Lat: 4.30241, Lng: -74.80412, Source: "exito"}, // dup (~mismo)
		{Name: "Olímpica", Lat: 4.31, Lng: -74.79, Source: "olimpica"},
	}
	out := DedupMarkets(in)
	require.Len(t, out, 2)
	assert.Equal(t, "osm", out[0].Source, "gana la primera fuente")
}

func TestSortMarketsByDistance_FiltersAndSorts(t *testing.T) {
	const lat, lng = 4.36, -74.80
	markets := []NearbyMarket{
		{Name: "Lejos", Lat: 4.50, Lng: -74.80},
		{Name: "Cerca", Lat: 4.362, Lng: -74.802},
		{Name: "Medio", Lat: 4.37, Lng: -74.80},
	}
	out := SortMarketsByDistance(markets, lat, lng, 5)
	require.Len(t, out, 2)
	assert.Equal(t, "Cerca", out[0].Market.Name)
	assert.Equal(t, "Medio", out[1].Market.Name)
}

func TestNearbyMarkets_MergesOSMandVTEX_AndCaches(t *testing.T) {
	osmCalls, getCalls := 0, 0
	overpass := func(ctx context.Context, q string) ([]byte, error) {
		osmCalls++
		return []byte(`{"elements":[{"type":"node","lat":4.361,"lon":-74.808,"tags":{"shop":"supermarket","name":"Supercundi"}}]}`), nil
	}
	httpGet := func(ctx context.Context, rawURL string) ([]byte, error) {
		getCalls++
		// Cualquier cadena VTEX devuelve una sede.
		return []byte(`{"items":[{"pickupPoint":{"friendlyName":"Exito Girardot","address":{"geoCoordinates":[-74.804,4.302]}}}]}`), nil
	}
	svc := NewPlacesServiceWithFetch(overpass, httpGet,
		[]vtexChain{{Chain: "exito", BaseURL: "https://x"}}, "")

	m, err := svc.NearbyMarkets(context.Background(), 4.36, -74.80, 5000)
	require.NoError(t, err)
	names := []string{}
	for _, x := range m {
		names = append(names, x.Name)
	}
	assert.Contains(t, strings.Join(names, ","), "Supercundi", "trae OSM")
	assert.Contains(t, strings.Join(names, ","), "Exito Girardot", "trae VTEX")

	// Segunda llamada misma celda → cache (no vuelve a llamar fuentes).
	_, err = svc.NearbyMarkets(context.Background(), 4.36, -74.80, 5000)
	require.NoError(t, err)
	assert.Equal(t, 1, osmCalls)
	assert.Equal(t, 1, getCalls)
}

func TestNearbyMarkets_AllSourcesFail_ReturnsError(t *testing.T) {
	fail := func(ctx context.Context, _ string) ([]byte, error) { return nil, assertErr }
	svc := NewPlacesServiceWithFetch(fail, fail,
		[]vtexChain{{Chain: "exito", BaseURL: "https://x"}}, "")
	_, err := svc.NearbyMarkets(context.Background(), 1, 1, 5000)
	assert.Error(t, err)
}

var assertErr = &simpleErr{"boom"}

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }
