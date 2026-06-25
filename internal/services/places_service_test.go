// Spec: specs/081-mercado-cercano-mapa/spec.md
package services

import (
	"context"
	"errors"
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
	  {"type":"node","lat":4.361,"lon":-74.808,"tags":{"shop":"supermarket","name":"D1","brand":"D1","addr:street":"Cra 7","addr:housenumber":"10-20","addr:city":"Fusagasugá"}},
	  {"type":"way","center":{"lat":4.362,"lon":-74.809},"tags":{"shop":"supermarket","name":"Ara"}},
	  {"type":"node","lat":4.363,"lon":-74.81,"tags":{"shop":"convenience"}},
	  {"type":"node","lat":0,"lon":0,"tags":{"shop":"supermarket","name":"Sin coords"}}
	]}`)
	markets := ParseOverpassMarkets(body)
	require.Len(t, markets, 2, "se omite el sin-nombre y el sin-coords")
	assert.Equal(t, "D1", markets[0].Name)
	assert.Equal(t, "Cra 7 10-20, Fusagasugá", markets[0].Address)
	assert.Equal(t, "Ara", markets[1].Name)
	assert.InDelta(t, 4.362, markets[1].Lat, 1e-9, "way usa center")
}

func TestParseOverpassMarkets_GarbageIsEmpty(t *testing.T) {
	assert.Empty(t, ParseOverpassMarkets([]byte("no-json")))
}

func TestSortMarketsByDistance_FiltersAndSorts(t *testing.T) {
	const lat, lng = 4.36, -74.80
	markets := []NearbyMarket{
		{Name: "Lejos", Lat: 4.50, Lng: -74.80},  // ~15+ km
		{Name: "Cerca", Lat: 4.362, Lng: -74.802}, // ~0.3 km
		{Name: "Medio", Lat: 4.37, Lng: -74.80},   // ~1.1 km
	}
	out := SortMarketsByDistance(markets, lat, lng, 5)
	require.Len(t, out, 2, "Lejos queda fuera del radio 5km")
	assert.Equal(t, "Cerca", out[0].Market.Name, "ordenado por distancia asc")
	assert.Equal(t, "Medio", out[1].Market.Name)
	assert.Less(t, out[0].DistKm, out[1].DistKm)
}

func TestNearbyMarkets_UsesFetchAndCaches(t *testing.T) {
	calls := 0
	fetch := func(ctx context.Context, q string) ([]byte, error) {
		calls++
		return []byte(`{"elements":[{"type":"node","lat":4.361,"lon":-74.808,"tags":{"shop":"supermarket","name":"Éxito"}}]}`), nil
	}
	svc := NewPlacesServiceWithFetch(fetch)
	m1, err := svc.NearbyMarkets(context.Background(), 4.36, -74.80, 5000)
	require.NoError(t, err)
	require.Len(t, m1, 1)
	assert.Equal(t, "Éxito", m1[0].Name)
	// Segunda llamada misma celda → cache, no vuelve a fetchear.
	_, err = svc.NearbyMarkets(context.Background(), 4.36, -74.80, 5000)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "segunda consulta sale de cache")
}

func TestNearbyMarkets_FetchErrorPropagates(t *testing.T) {
	fetch := func(ctx context.Context, q string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	svc := NewPlacesServiceWithFetch(fetch)
	_, err := svc.NearbyMarkets(context.Background(), 1, 1, 5000)
	assert.Error(t, err)
}
