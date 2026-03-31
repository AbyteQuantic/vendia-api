package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StorageService uploads files to Supabase Storage.
// It uses the Supabase REST API (no AWS SDK needed).
// Drop-in replacement for R2Service — same Upload/Download interface.
type StorageService struct {
	supabaseURL string
	serviceKey  string
	publicURL   string
	client      *http.Client
}

func NewStorageService(supabaseURL, serviceKey string) *StorageService {
	return &StorageService{
		supabaseURL: supabaseURL,
		serviceKey:  serviceKey,
		publicURL:   supabaseURL + "/storage/v1/object/public",
		client:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Upload uploads a file to Supabase Storage.
// bucket: "product-photos", key: "products/tenant-uuid/product-uuid.webp"
func (s *StorageService) Upload(ctx context.Context, bucket, key string, data []byte, contentType string) (string, error) {
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.supabaseURL, bucket, key)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("storage upload: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.serviceKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-upsert", "true") // overwrite if exists

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("storage upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("storage upload returned %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	publicURL := fmt.Sprintf("%s/%s/%s", s.publicURL, bucket, key)
	return publicURL, nil
}

// Download downloads a file from Supabase Storage.
func (s *StorageService) Download(ctx context.Context, bucket, key string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.supabaseURL, bucket, key)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.serviceKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("storage download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("storage download returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	return data, resp.Header.Get("Content-Type"), nil
}
