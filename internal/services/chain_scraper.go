// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// ChainSource — una cadena scrapeable (plataforma VTEX con API pública JSON).
type ChainSource struct {
	Chain   string // exito | olimpica
	BaseURL string // https://www.exito.com
}

// DefaultChainSources — cadenas con e-commerce VTEX real (Éxito, Olímpica). D1/
// Ara/Maximercado NO tienen catálogo online por SKU (folleto PDF, fuera de aquí).
var DefaultChainSources = []ChainSource{
	{Chain: "exito", BaseURL: "https://www.exito.com"},
	{Chain: "olimpica", BaseURL: "https://www.olimpica.com"},
}

// ScrapeTerms — insumos comunes de tienda/restaurante a consultar.
var ScrapeTerms = []string{
	"arroz", "aceite", "papa", "cebolla", "tomate", "huevos", "lentejas",
	"frijol", "panela", "leche", "azucar", "sal", "pasta", "atun", "cafe",
	"harina", "platano", "yuca", "zanahoria", "ajo", "queso", "mantequilla",
}

// nonFoodCat — subcadenas de CATEGORÍA que delatan productos NO comestibles que
// el full-text VTEX trae como homónimos (aceite capilar, vaporera de huevos,
// olla arrocera, etc.). Se filtran al scrapear y al consultar.
var nonFoodCat = []string{
	"corporal", "capilar", "utensilio", "accesorio", "procesador", "superficie",
	"sexual", "gel", "jabon", "sarten", "caja", "canasta", "olla", "crispetera",
	"fuente", "bicicleta", "molde", "refract", "juguet", "moto", "herramient",
	"mascota", "aseo", "hogar", "belleza", "tecnolog", "ferreter", "papeler",
	"deporte", "ropa", "calzado", "electrodom", "triturad", "organizad",
	"vaporera", "hervidor", "arrocera", "facial", "maquilla", "perfum",
	"labial", "balsamo", "locion", "shampoo", "acondicionador", "desodorante",
	"cuidado", "manos y cuerpo", "pañal", "panal de bebe", "higiene",
}

// IsFoodCategory reporta si una categoría parece comestible (no está en la lista
// negra). Vacío = se acepta (mejor incluir que excluir un alimento).
func IsFoodCategory(category string) bool {
	c := NormalizeText(category)
	for _, bad := range nonFoodCat {
		if strings.Contains(c, bad) {
			return false
		}
	}
	return true
}

// vtexProduct — shape mínimo de la API de catálogo VTEX.
type vtexProduct struct {
	ProductName string   `json:"productName"`
	Brand       string   `json:"brand"`
	LinkText    string   `json:"linkText"`
	Categories  []string `json:"categories"`
	Items       []struct {
		ItemID  string `json:"itemId"`
		EAN     string `json:"ean"`
		Sellers []struct {
			CommertialOffer struct {
				Price     float64 `json:"Price"`
				ListPrice float64 `json:"ListPrice"`
			} `json:"commertialOffer"`
		} `json:"sellers"`
	} `json:"items"`
}

// FetchVTEXProducts consulta la API pública de catálogo VTEX por un término.
func FetchVTEXProducts(client *http.Client, baseURL, term string) ([]vtexProduct, error) {
	u := fmt.Sprintf("%s/api/catalog_system/pub/products/search?ft=%s&_from=0&_to=49", baseURL, term)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; VendIA/1.0)")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s → %d", baseURL, term, resp.StatusCode)
	}
	var products []vtexProduct
	if err := json.NewDecoder(resp.Body).Decode(&products); err != nil {
		return nil, err
	}
	return products, nil
}

// ScrapeChainsForCity scrapea las cadenas para una ciudad y guarda filas nuevas
// en chain_price (append-only). Tolerante a fallos por cadena/término. Devuelve
// cuántas filas insertó.
func ScrapeChainsForCity(db *gorm.DB, city string, sources []ChainSource) int {
	client := &http.Client{Timeout: 15 * time.Second}
	now := time.Now()
	inserted := 0
	for _, src := range sources {
		for _, term := range ScrapeTerms {
			products, err := FetchVTEXProducts(client, src.BaseURL, term)
			if err != nil || len(products) == 0 {
				continue // best-effort
			}
			rows := make([]models.ChainPrice, 0, len(products))
			for _, p := range products {
				price := vtexPrice(p)
				if price <= 0 {
					continue
				}
				cat := ""
				if len(p.Categories) > 0 {
					cat = p.Categories[0]
				}
				if !IsFoodCategory(cat) || price < 300 || price > 300000 {
					continue // descarta no-comestibles y precios atípicos
				}
				rows = append(rows, models.ChainPrice{
					Chain: src.Chain, City: city,
					RawName: p.ProductName, NormalizedName: NormalizeText(p.ProductName),
					Brand: p.Brand, Price: price, ListPrice: vtexListPrice(p),
					Category: cat, SKU: vtexSKU(p), URL: src.BaseURL + "/" + p.LinkText + "/p",
					ScrapedAt: now,
				})
			}
			if len(rows) > 0 {
				if err := db.CreateInBatches(rows, 100).Error; err == nil {
					inserted += len(rows)
				}
			}
			time.Sleep(400 * time.Millisecond) // cortesía con la cadena
		}
	}
	return inserted
}

func vtexPrice(p vtexProduct) float64 {
	if len(p.Items) > 0 && len(p.Items[0].Sellers) > 0 {
		return p.Items[0].Sellers[0].CommertialOffer.Price
	}
	return 0
}

func vtexListPrice(p vtexProduct) float64 {
	if len(p.Items) > 0 && len(p.Items[0].Sellers) > 0 {
		return p.Items[0].Sellers[0].CommertialOffer.ListPrice
	}
	return 0
}

func vtexSKU(p vtexProduct) string {
	if len(p.Items) > 0 {
		return p.Items[0].ItemID
	}
	return ""
}
