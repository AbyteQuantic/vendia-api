// Spec: specs/072-captura-ubicacion-gps-osm/spec.md
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Geocoder convierte coordenadas en una etiqueta legible + la ciudad. Interfaz
// pequeña e inyectable (los handlers la reciben; en tests se usa un fake).
type Geocoder interface {
	Reverse(lat, lng float64) (label, city string, err error)
}

// NominatimGeocoder usa el reverse-geocoding GRATIS de OpenStreetMap (Nominatim).
// Política: 1 req/seg, User-Agent identificable, atribución OSM. Sin Google.
type NominatimGeocoder struct {
	BaseURL   string // default https://nominatim.openstreetmap.org
	UserAgent string
	Client    *http.Client
}

func NewNominatimGeocoder() *NominatimGeocoder {
	return &NominatimGeocoder{
		BaseURL:   "https://nominatim.openstreetmap.org",
		UserAgent: "VendIA/1.0 (https://vendia.store)",
		Client:    &http.Client{Timeout: 6 * time.Second},
	}
}

type nominatimResp struct {
	DisplayName string `json:"display_name"`
	Address     struct {
		City         string `json:"city"`
		Town         string `json:"town"`
		Municipality string `json:"municipality"`
		Village      string `json:"village"`
		County       string `json:"county"`
	} `json:"address"`
}

func (g *NominatimGeocoder) Reverse(lat, lng float64) (string, string, error) {
	u := fmt.Sprintf("%s/reverse?lat=%f&lon=%f&format=jsonv2&accept-language=es&zoom=14",
		strings.TrimRight(g.BaseURL, "/"), lat, lng)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", g.UserAgent)
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("nominatim status %d", resp.StatusCode)
	}
	var nr nominatimResp
	if err := json.NewDecoder(resp.Body).Decode(&nr); err != nil {
		return "", "", err
	}
	city := firstNonEmpty(nr.Address.City, nr.Address.Town, nr.Address.Municipality,
		nr.Address.Village, nr.Address.County)
	return nr.DisplayName, city, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
