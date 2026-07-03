// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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
	// Reforzado (Spec 077, 2026-06-21): cosméticos/no-comestibles que se colaban
	// (aceite de coco corporal, anti-estrías, CBD, bronceador, dispensadores…).
	"humectante", "bronceador", "exfoliante", "dolor", "droga", "consola",
	"videojuego", "cannabis", "estria", "ducha", "exomega", "dispensador",
	"medicament", "vitamina", "suplemento", "bebe", "infantil", "mama",
	// 2ª ronda (aguacate→mordedor/peluche/pestañina/secador): juguetería,
	// peluches, mordedores, maquillaje y menaje que el full-text traía.
	"peluche", "pestanina", "secador", "rasca encia", "mordedor", "llamadiente",
	"cabello", "pintura", "decoracion", "vela", "manualidad", "fiesta",
	"menaje", "desechable", "papeleria", "marroquineria",
}

// nonFoodName — palabras en el NOMBRE que delatan no-comestible aunque la
// categoría diga "Aceites" (Olímpica mete cosméticos ahí). Spec 077.
var nonFoodName = []string{
	"corporal", "capilar", "anti estria", "antiestria", "estria", "bronceador",
	"cannabis", "cbd", "drogam", "exomega", "ducha", "dispensador", "humectacion",
	"masaje", "facial", "piel de oro", "a derma", "para piel", "bebe",
	// Aceites automotrices/industriales (no de cocina) que caen en "Aceites".
	"lubricante", "aditivo", "probador", "aerosol", "qualitor", "autool",
	"motor", "anticorrosiv", "penetrante", "gotero",
	// Juguetes/menaje/maquillaje que comparten nombre con una fruta/insumo.
	"peluche", "mordedor", "llamadiente", "cortador", "cepillo", "moldeador",
	"pestanina", "secador", "exprimidor", "rallador", "molde",
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

// isFoodProduct combina la categoría Y el nombre: descarta un cosmético aunque
// caiga en una categoría comestible (homónimos de aceite/crema).
func isFoodProduct(rawName, category string) bool {
	if !IsFoodCategory(category) {
		return false
	}
	n := NormalizeText(rawName)
	for _, bad := range nonFoodName {
		if strings.Contains(n, bad) {
			return false
		}
	}
	return true
}

// packRe — número + unidad de volumen/peso en el nombre (ej "1 Lt", "900ml",
// "500 g", "2 kg"). El \b evita capturar dentro de otra palabra.
var packRe = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(ml|cc|lt|litros?|l|kg|kilos?|gr?|g)\b`)

// parsePackaging extrae cantidad+unidad del nombre y la NORMALIZA a una unidad
// base común (ml para volumen, g para peso) para que el precio por unidad base
// sea comparable entre productos (1 L y 900 ml comparables). El precio por base
// = precio del empaque / cantidad en base. Devuelve unit="" si no se reconoce.
func parsePackaging(rawName string, price float64) (unit string, packQty, pricePerBase float64) {
	ms := packRe.FindAllStringSubmatch(strings.ToLower(rawName), -1)
	if len(ms) == 0 {
		return "", 0, 0
	}
	m := ms[len(ms)-1] // la última suele ser el tamaño del empaque
	qty, err := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
	if err != nil || qty <= 0 {
		return "", 0, 0
	}
	switch strings.ToLower(m[2]) {
	case "ml", "cc":
		unit, packQty = "ml", qty
	case "l", "lt", "litro", "litros":
		unit, packQty = "ml", qty*1000
	case "g", "gr":
		unit, packQty = "g", qty
	case "kg", "kilo", "kilos":
		unit, packQty = "g", qty*1000
	default:
		return "", 0, 0
	}
	if packQty > 0 {
		pricePerBase = price / packQty
	}
	return unit, packQty, pricePerBase
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
//
// Auditoría 2026-07-03: term se interpolaba crudo en la URL (fmt.Sprintf sin
// escapar). Hoy es inofensivo porque siempre viene de ScrapeTerms (22
// palabras fijas, sin espacios ni caracteres especiales), pero esta función
// es exportada y reutilizable — si en el futuro se le pasa un término con
// espacio, '&', '#' o similar (ej. una búsqueda libre del tendero), rompería
// la URL o inyectaría parámetros extra a la API de VTEX. url.QueryEscape lo
// vuelve seguro sin cambiar el comportamiento actual (los 22 términos
// hardcodeados no tienen caracteres a escapar).
func FetchVTEXProducts(client *http.Client, baseURL, term string) ([]vtexProduct, error) {
	u := fmt.Sprintf("%s/api/catalog_system/pub/products/search?ft=%s&_from=0&_to=49",
		baseURL, url.QueryEscape(term))
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
	first := true
	for _, src := range sources {
		for _, term := range ScrapeTerms {
			// Auditoría 2026-07-03: la pausa de cortesía vivía DESPUÉS del
			// bloque de inserción, así que un `continue` por error/vacío la
			// saltaba por completo — si la cadena empieza a bloquear/limitar
			// al scraper (justo el escenario que la pausa buscaba evitar),
			// el loop pasaba los 22 términos restantes SIN ninguna espera,
			// lo que agrava el bloqueo en vez de mitigarlo. Ahora la pausa
			// corre SIEMPRE antes de cada request (salvo la primera de
			// todas), pase lo que pase con la anterior.
			if !first {
				time.Sleep(400 * time.Millisecond)
			}
			first = false

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
				if !isFoodProduct(p.ProductName, cat) || price < 300 || price > 300000 {
					continue // descarta no-comestibles (categoría/nombre) y atípicos
				}
				unit, packQty, perBase := parsePackaging(p.ProductName, price)
				rows = append(rows, models.ChainPrice{
					Chain: src.Chain, City: city,
					RawName: p.ProductName, NormalizedName: NormalizeText(p.ProductName),
					Brand: p.Brand, Price: price, ListPrice: vtexListPrice(p),
					Unit: unit, PackQty: packQty, PricePerBaseUnit: perBase,
					Category: cat, SKU: vtexSKU(p), URL: src.BaseURL + "/" + p.LinkText + "/p",
					ScrapedAt: now,
				})
			}
			if len(rows) > 0 {
				if err := db.CreateInBatches(rows, 100).Error; err == nil {
					inserted += len(rows)
				}
			}
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
