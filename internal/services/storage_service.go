package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// StorageService uploads files to Supabase Storage via its REST API.
// It's a drop-in for FileStorage (same Upload / Download / Delete
// shape as R2Service) — we picked Supabase because it ships with the
// Postgres we already use and avoids a second vendor.
//
// Production bug history (2026-04):
//   - "no se pudo guardar el banner" was actually Supabase returning
//     400 "Bucket not found" because `promo-banners` was never
//     provisioned. We now auto-create the bucket (public, idempotent)
//     the first time we upload into it and log the raw upstream body
//     on every failure so future regressions are visible.
//   - The previous HEAD post-upload verification was too strict: a
//     non-public bucket or a CDN propagation delay caused the whole
//     request to 500 even when the object was already on disk. It's
//     now a warning log, not a hard failure.
type StorageService struct {
	supabaseURL string
	serviceKey  string
	publicURL   string
	client      *http.Client

	// ensuredBuckets memoises which buckets we've already verified /
	// created in this process so we don't hit the bucket API on every
	// upload (it's cheap, but N extra HTTP calls per banner add up).
	ensuredMu      sync.Mutex
	ensuredBuckets map[string]bool
}

func NewStorageService(supabaseURL, serviceKey string) *StorageService {
	return &StorageService{
		supabaseURL:    supabaseURL,
		serviceKey:     serviceKey,
		publicURL:      supabaseURL + "/storage/v1/object/public",
		client:         &http.Client{Timeout: 30 * time.Second},
		ensuredBuckets: map[string]bool{},
	}
}

// ensureBucket makes sure `bucket` exists and is public. Idempotent:
// a second call in the same process is a no-op (in-memory cache);
// across processes it's still safe because Supabase returns 409 on
// duplicate creation, which we treat as success.
func (s *StorageService) ensureBucket(ctx context.Context, bucket string) error {
	s.ensuredMu.Lock()
	if s.ensuredBuckets[bucket] {
		s.ensuredMu.Unlock()
		return nil
	}
	s.ensuredMu.Unlock()

	// GET first — the cheap happy path.
	getURL := fmt.Sprintf("%s/storage/v1/bucket/%s", s.supabaseURL, bucket)
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return fmt.Errorf("ensureBucket: build GET: %w", err)
	}
	getReq.Header.Set("Authorization", "Bearer "+s.serviceKey)
	getReq.Header.Set("apikey", s.serviceKey)
	getResp, err := s.client.Do(getReq)
	if err != nil {
		return fmt.Errorf("ensureBucket: GET %s: %w", bucket, err)
	}
	getResp.Body.Close()

	if getResp.StatusCode == http.StatusOK {
		s.ensuredMu.Lock()
		s.ensuredBuckets[bucket] = true
		s.ensuredMu.Unlock()
		return nil
	}
	if getResp.StatusCode != http.StatusNotFound {
		// Not 200 and not 404 — permission problem, auth problem, etc.
		// Log it but still try to create so the error surfaces there
		// with a more descriptive body.
		log.Printf("[STORAGE] ensureBucket %s: unexpected GET status %d", bucket, getResp.StatusCode)
	}

	// Create with public read — banners, logos and enhanced product
	// photos all need to be reachable by the public web catalog.
	createBody, _ := json.Marshal(map[string]any{
		"id":     bucket,
		"name":   bucket,
		"public": true,
	})
	createURL := fmt.Sprintf("%s/storage/v1/bucket", s.supabaseURL)
	createReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, createURL, bytes.NewReader(createBody))
	if err != nil {
		return fmt.Errorf("ensureBucket: build POST: %w", err)
	}
	createReq.Header.Set("Authorization", "Bearer "+s.serviceKey)
	createReq.Header.Set("apikey", s.serviceKey)
	createReq.Header.Set("Content-Type", "application/json")

	createResp, err := s.client.Do(createReq)
	if err != nil {
		return fmt.Errorf("ensureBucket: POST %s: %w", bucket, err)
	}
	defer createResp.Body.Close()

	// 200/201 → created; 409 → someone else created it first, which
	// is exactly what we wanted. Anything else is a real failure.
	if createResp.StatusCode == http.StatusOK ||
		createResp.StatusCode == http.StatusCreated ||
		createResp.StatusCode == http.StatusConflict {
		log.Printf("[STORAGE] bucket %q ready (status=%d)", bucket, createResp.StatusCode)
		s.ensuredMu.Lock()
		s.ensuredBuckets[bucket] = true
		s.ensuredMu.Unlock()
		return nil
	}

	body, _ := io.ReadAll(createResp.Body)
	return fmt.Errorf(
		"ensureBucket: create %s returned %d: %s",
		bucket, createResp.StatusCode, truncateBytes(body, 400))
}

// Upload pushes a binary file to Supabase Storage and returns its
// public URL. Logs the full upstream body on failure so production
// errors stop being generic.
//
// bucket: "promo-banners", key: "<tenant>/<uuid>.png"
func (s *StorageService) Upload(ctx context.Context, bucket, key string, data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("storage upload: empty data, nothing to upload")
	}

	// Ensure bucket *before* the actual PUT — previously we relied on
	// the bucket being provisioned manually, which silently broke
	// every fresh tenant and produced the famous "no se pudo guardar
	// el banner" 500.
	if err := s.ensureBucket(ctx, bucket); err != nil {
		log.Printf("[STORAGE] ERROR REAL UPLOAD (ensureBucket) bucket=%s key=%s: %v",
			bucket, key, err)
		return "", fmt.Errorf("storage upload: %w", err)
	}

	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.supabaseURL, bucket, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("storage upload: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.serviceKey)
	req.Header.Set("apikey", s.serviceKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-upsert", "true") // overwrite on duplicate key

	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("[STORAGE] ERROR REAL UPLOAD (network) bucket=%s key=%s bytes=%d: %v",
			bucket, key, len(data), err)
		return "", fmt.Errorf("storage upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		log.Printf(
			"[STORAGE] ERROR REAL UPLOAD (status=%d) bucket=%s key=%s ctype=%s bytes=%d body=%s",
			resp.StatusCode, bucket, key, contentType, len(data), truncateBytes(body, 500))
		return "", fmt.Errorf(
			"storage upload returned %d: %s",
			resp.StatusCode, truncateBytes(body, 200))
	}

	publicURL := fmt.Sprintf("%s/%s/%s", s.publicURL, bucket, key)

	// Post-upload HEAD verification is best-effort. A 404 here used
	// to fail the whole request, but Supabase/Cloudflare CDN can take
	// a few hundred ms to propagate the new object and the upload
	// itself already succeeded (we got 200 above). We log the
	// anomaly but return the URL — the frontend's Image widget will
	// retry naturally on render.
	go func() {
		verifyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		verifyReq, verifyErr := http.NewRequestWithContext(
			verifyCtx, http.MethodHead, publicURL, nil)
		if verifyErr != nil {
			return
		}
		verifyResp, verifyErr := s.client.Do(verifyReq)
		if verifyErr != nil {
			log.Printf("[STORAGE] HEAD verify skipped (%v) url=%s", verifyErr, publicURL)
			return
		}
		verifyResp.Body.Close()
		if verifyResp.StatusCode != http.StatusOK {
			log.Printf(
				"[STORAGE] HEAD verify warning: uploaded OK but HEAD returned %d (url=%s). Bucket may not be public or CDN still warming.",
				verifyResp.StatusCode, publicURL)
		}
	}()

	return publicURL, nil
}

// Delete removes a file from Supabase Storage.
func (s *StorageService) Delete(ctx context.Context, bucket, key string) error {
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.supabaseURL, bucket, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("storage delete: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.serviceKey)
	req.Header.Set("apikey", s.serviceKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("storage delete failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("storage delete returned %d: %s",
			resp.StatusCode, truncateBytes(body, 200))
	}
	return nil
}

// Download fetches a file from Supabase Storage.
func (s *StorageService) Download(ctx context.Context, bucket, key string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.supabaseURL, bucket, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.serviceKey)
	req.Header.Set("apikey", s.serviceKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("storage download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf(
			"storage download returned %d: %s",
			resp.StatusCode, truncateBytes(body, 200))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	return data, resp.Header.Get("Content-Type"), nil
}

// truncateBytes returns a short prefix of b — useful for log lines
// where we don't want to dump a multi-MB error body but do want the
// first sentence of the upstream message.
//
// Different name from [truncate] in backup_service.go on purpose:
// this one operates on []byte and appends an ellipsis, the other is
// a simple rune-unsafe string slice with no trailing marker.
func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
