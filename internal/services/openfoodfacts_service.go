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
