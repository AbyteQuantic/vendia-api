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

// PhotonGeocoder usa el reverse-geocoding GRATIS de Photon (Komoot, basado en
// OSM). A diferencia de Nominatim, Photon NO prohíbe el uso desde servidores
// cloud (como Render), por eso es el fallback de servidor. Sin Google, sin llave.
// Es solo FALLBACK: la ruta primaria es la ciudad que manda el cliente (geocoder
// nativo del móvil).
type PhotonGeocoder struct {
	BaseURL string // default https://photon.komoot.io
	Client  *http.Client
}

func NewPhotonGeocoder() *PhotonGeocoder {
	return &PhotonGeocoder{
		BaseURL: "https://photon.komoot.io",
		Client:  &http.Client{Timeout: 6 * time.Second},
	}
}

type photonResp struct {
	Features []struct {
		Properties struct {
			Name     string `json:"name"`
			City     string `json:"city"`
			District string `json:"district"`
			County   string `json:"county"`
			State    string `json:"state"`
		} `json:"properties"`
	} `json:"features"`
}

func (g *PhotonGeocoder) Reverse(lat, lng float64) (string, string, error) {
	u := fmt.Sprintf("%s/reverse?lat=%f&lon=%f&lang=es",
		strings.TrimRight(g.BaseURL, "/"), lat, lng)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "VendIA/1.0 (https://vendia.store)")
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("photon status %d", resp.StatusCode)
	}
	var pr photonResp
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", "", err
	}
	if len(pr.Features) == 0 {
		return "", "", fmt.Errorf("photon sin resultados")
	}
	p := pr.Features[0].Properties
	city := firstNonEmpty(p.City, p.District, p.County)
	label := firstNonEmpty(p.Name, p.City, p.County)
	return label, city, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
