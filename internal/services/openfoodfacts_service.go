package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenFoodFactsService struct {
	baseURL string
	client  *http.Client
}

func NewOpenFoodFactsService() *OpenFoodFactsService {
	return &OpenFoodFactsService{
		baseURL: "https://world.openfoodfacts.org/api/v2/product",
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

type OFFProduct struct {
	Name     string `json:"name"`
	Brand    string `json:"brand,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Barcode  string `json:"barcode"`
	Category string `json:"category,omitempty"`
	Quantity string `json:"quantity,omitempty"`
}

type offAPIResponse struct {
	Status  int `json:"status"`
	Product struct {
		ProductName   string `json:"product_name"`
		Brands        string `json:"brands"`
		ImageFrontURL string `json:"image_front_url"`
		Categories    string `json:"categories"`
		Quantity      string `json:"quantity"`
	} `json:"product"`
}

type offSearchResponse struct {
	Products []struct {
		ProductName   string `json:"product_name"`
		Brands        string `json:"brands"`
		ImageSmallURL string `json:"image_small_url"`
	} `json:"products"`
}

func (s *OpenFoodFactsService) SearchProducts(ctx context.Context, query string, limit int) ([]OFFProduct, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	url := fmt.Sprintf("https://world.openfoodfacts.org/cgi/search.pl?search_terms=%s&search_simple=1&action=process&json=1&page_size=%d&fields=product_name,image_small_url,brands", query, limit)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "VendIA-POS/1.0 (vendia.co)")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OFF search failed: %w", err)
	}
	defer resp.Body.Close()

	// Fallback: try v2 API if CGI returns non-JSON
	if resp.StatusCode != 200 {
		return s.searchV2(ctx, query, limit)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Check if response is HTML instead of JSON
	if len(body) > 0 && body[0] == '<' {
		return s.searchV2(ctx, query, limit)
	}

	var result offSearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return s.searchV2(ctx, query, limit)
	}

	var products []OFFProduct
	for _, p := range result.Products {
		if p.ProductName == "" {
			continue
		}
		products = append(products, OFFProduct{
			Name:     p.ProductName,
			Brand:    p.Brands,
			ImageURL: p.ImageSmallURL,
		})
	}
	return products, nil
}

func (s *OpenFoodFactsService) searchV2(ctx context.Context, query string, limit int) ([]OFFProduct, error) {
	url := fmt.Sprintf("https://world.openfoodfacts.org/api/v2/search?search_terms=%s&fields=product_name,image_small_url,brands&page_size=%d", query, limit)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "VendIA-POS/1.0 (vendia.co)")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result offSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var products []OFFProduct
	for _, p := range result.Products {
		if p.ProductName == "" {
			continue
		}
		products = append(products, OFFProduct{
			Name:     p.ProductName,
			Brand:    p.Brands,
			ImageURL: p.ImageSmallURL,
		})
	}
	return products, nil
}

func (s *OpenFoodFactsService) LookupBarcode(ctx context.Context, barcode string) (*OFFProduct, error) {
	url := fmt.Sprintf("%s/%s.json", s.baseURL, barcode)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "VendIA-POS/1.0 (vendia.co)")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Open Food Facts request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read OFF response: %w", err)
	}

	var apiResp offAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse OFF response: %w", err)
	}

	if apiResp.Status != 1 {
		return nil, nil
	}

	return &OFFProduct{
		Name:     apiResp.Product.ProductName,
		Brand:    apiResp.Product.Brands,
		ImageURL: apiResp.Product.ImageFrontURL,
		Barcode:  barcode,
		Category: apiResp.Product.Categories,
		Quantity: apiResp.Product.Quantity,
	}, nil
}
