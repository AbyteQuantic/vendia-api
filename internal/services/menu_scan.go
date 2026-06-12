// Spec: specs/043-menu-restaurante-recetas/spec.md
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// MenuDish is one dish extracted from a restaurant menu photo.
type MenuDish struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Portion     string  `json:"portion"`
	Category    string  `json:"category"`
}

// MenuScanResult is the structured output of scanning a menu photo.
type MenuScanResult struct {
	Dishes []MenuDish `json:"dishes"`
}

// ScanMenu reads a photo of a restaurant MENU (carta) and extracts the dishes
// with their name, description, sale price, portion and category — so the
// organizer can review/edit and publish them to the "Menú restaurante" section.
// Mirrors ScanInvoice (low-temp multimodal + strict JSON), but with a
// menu-specific prompt (not an invoice/OCR-of-products prompt).
func (s *GeminiService) ScanMenu(ctx context.Context, imageData []byte, mimeType string) (*MenuScanResult, error) {
	prompt := `Eres un asistente que LEE LA CARTA/MENÚ de un restaurante en una foto y extrae los PLATOS para cargarlos al catálogo.

REGLAS:
1. Extrae SOLO los platos/bebidas que aparecen ESCRITOS en la carta de la imagen. PROHIBIDO inventar platos que no estén impresos.
2. "name": el nombre del plato tal como aparece (ej: "Bandeja Paisa", "Limonada de Coco", "Hamburguesa Clásica").
3. "description": la descripción/ingredientes que acompaña al plato en la carta, si la hay (ej: "Frijoles, arroz, carne molida, chicharrón, huevo, arepa"). Si no hay descripción impresa, deja "".
4. "price": el PRECIO de venta impreso, como número entero en pesos (ej: 25000). Sin símbolos ni puntos de miles. Si un plato no tiene precio visible, pon 0.
5. "portion": tamaño/porción si aparece (ej: "Personal", "Para compartir", "Mediana", "12 oz"). Si no, deja "".
6. "category": clasifica el plato en una de: "Entradas", "Platos fuertes", "Bebidas", "Postres", "Adiciones", "Otros". Usa tu mejor criterio.
7. Si un renglón está borroso o cortado y no lo puedes leer con certeza, IGNÓRALO.

Retorna JSON ESTRICTO sin markdown:
{"dishes":[{"name":"nombre del plato","description":"descripción si la hay","price":0,"portion":"","category":"Platos fuertes"}]}

Si la imagen NO es una carta/menú o no tiene platos legibles, retorna: {"dishes":[]}

RETURN ONLY RAW JSON. DO NOT WRAP IN MARKDOWN. NO EXPLANATIONS OUTSIDE THE JSON.`

	text, err := s.callWithImageLowTemp(ctx, imageData, mimeType, prompt)
	if err != nil {
		return nil, err
	}
	log.Printf("[MENU-SCAN] Raw AI response (%d chars): %.400s", len(text), text)

	text = stripMarkdownJSON(text)
	if text == "" {
		return &MenuScanResult{Dishes: nil}, nil
	}

	var result MenuScanResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		log.Printf("[MENU-SCAN] JSON parse error: %v | cleaned: %.300s", err, text)
		return nil, fmt.Errorf("no se pudo interpretar el menú de la imagen: %w", err)
	}
	return &result, nil
}
