package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// BackupService mirrors Supabase Storage buckets into a parallel *-backup
// bucket once per day so that even an accidental full wipe of the primary
// bucket is recoverable for at least one cycle. It is intentionally simple:
// list → copy-if-missing → move on. The copy uses Supabase's server-side
// /object/copy endpoint so we never pay egress to ship bytes through the
// Render node.
type BackupService struct {
	supabaseURL string
	serviceKey  string
	client      *http.Client
	// mirrors maps sourceBucket → destBucket. Populated in NewBackupService.
	mirrors map[string]string
}

// BackupPairs is the canonical list of source→backup buckets for VendIA.
func BackupPairs() map[string]string {
	return map[string]string{
		"product-photos": "product-photos-backup",
		"store-logos":    "store-logos-backup",
		"vendia-logos":   "vendia-logos-backup",
	}
}

func NewBackupService(supabaseURL, serviceKey string) *BackupService {
	return &BackupService{
		supabaseURL: strings.TrimRight(supabaseURL, "/"),
		serviceKey:  serviceKey,
		client:      &http.Client{Timeout: 30 * time.Second},
		mirrors:     BackupPairs(),
	}
}

// EnsureBackupBuckets creates *-backup buckets if they don't already exist.
// Idempotent — conflicts are treated as "already there".
func (b *BackupService) EnsureBackupBuckets(ctx context.Context) error {
	for _, dest := range b.mirrors {
		if err := b.createBucketIfMissing(ctx, dest); err != nil {
			// Don't fail the whole boot over one bucket — just log.
			log.Printf("[BACKUP] ensure bucket %s: %v", dest, err)
		}
	}
	return nil
}

func (b *BackupService) createBucketIfMissing(ctx context.Context, bucket string) error {
	body, _ := json.Marshal(map[string]any{
		"id":     bucket,
		"name":   bucket,
		"public": false, // backups never need to be public
	})
	url := b.supabaseURL + "/storage/v1/bucket"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.serviceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 200/201: created. 409: already exists. Anything else: surface.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusConflict {
		return nil
	}
	msg, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("bucket create %d: %s", resp.StatusCode, truncate(string(msg), 200))
}

// MirrorStats summarizes one mirror pass.
type MirrorStats struct {
	Source   string
	Dest     string
	Listed   int
	Copied   int
	Skipped  int
	Failures int
}

// MirrorAll runs one pass across every configured bucket pair.
func (b *BackupService) MirrorAll(ctx context.Context) []MirrorStats {
	stats := make([]MirrorStats, 0, len(b.mirrors))
	for src, dst := range b.mirrors {
		s, err := b.mirrorBucket(ctx, src, dst)
		if err != nil {
			log.Printf("[BACKUP] mirror %s → %s: %v", src, dst, err)
		}
		stats = append(stats, s)
	}
	return stats
}

type storageObject struct {
	Name string `json:"name"`
}

func (b *BackupService) mirrorBucket(ctx context.Context, source, dest string) (MirrorStats, error) {
	stats := MirrorStats{Source: source, Dest: dest}

	objects, err := b.listAll(ctx, source, "")
	if err != nil {
		return stats, fmt.Errorf("list %s: %w", source, err)
	}
	stats.Listed = len(objects)

	for _, obj := range objects {
		if obj == "" {
			continue
		}
		exists, err := b.headObject(ctx, dest, obj)
		if err != nil {
			stats.Failures++
			continue
		}
		if exists {
			stats.Skipped++
			continue
		}
		if err := b.copyObject(ctx, source, dest, obj); err != nil {
			log.Printf("[BACKUP] copy %s/%s → %s/%s: %v", source, obj, dest, obj, err)
			stats.Failures++
			continue
		}
		stats.Copied++
	}

	if stats.Listed > 0 {
		log.Printf("[BACKUP] %s → %s: listed=%d copied=%d skipped=%d failures=%d",
			source, dest, stats.Listed, stats.Copied, stats.Skipped, stats.Failures)
	}
	return stats, nil
}

// listAll recursively lists every object path inside a bucket. Supabase's
// list endpoint only returns one folder level per call, so we walk it.
func (b *BackupService) listAll(ctx context.Context, bucket, prefix string) ([]string, error) {
	out := []string{}
	var walk func(p string) error
	walk = func(p string) error {
		page := 0
		for {
			items, err := b.listPage(ctx, bucket, p, page)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return nil
			}
			for _, it := range items {
				if it.Name == "" {
					continue
				}
				childPath := it.Name
				if p != "" {
					childPath = p + "/" + it.Name
				}
				// Folders come back without an "id"; treat any name without a
				// dot as a directory to recurse. Supabase list API returns
				// id=null for folders, but we don't read that field here —
				// calling list on a file simply returns empty, which is safe.
				if strings.Contains(it.Name, ".") {
					out = append(out, childPath)
				} else {
					if err := walk(childPath); err != nil {
						return err
					}
				}
			}
			if len(items) < 100 {
				return nil
			}
			page++
		}
	}
	err := walk(prefix)
	return out, err
}

func (b *BackupService) listPage(ctx context.Context, bucket, prefix string, page int) ([]storageObject, error) {
	body, _ := json.Marshal(map[string]any{
		"prefix": prefix,
		"limit":  100,
		"offset": page * 100,
		"sortBy": map[string]string{"column": "name", "order": "asc"},
	})
	url := fmt.Sprintf("%s/storage/v1/object/list/%s", b.supabaseURL, bucket)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.serviceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list %d: %s", resp.StatusCode, truncate(string(msg), 200))
	}
	var items []storageObject
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	return items, nil
}

func (b *BackupService) headObject(ctx context.Context, bucket, key string) (bool, error) {
	url := fmt.Sprintf("%s/storage/v1/object/%s/%s", b.supabaseURL, bucket, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+b.serviceKey)
	resp, err := b.client.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusBadRequest:
		return false, nil
	default:
		return false, fmt.Errorf("head %d", resp.StatusCode)
	}
}

func (b *BackupService) copyObject(ctx context.Context, source, dest, key string) error {
	body, _ := json.Marshal(map[string]any{
		"bucketId":       source,
		"sourceKey":      key,
		"destinationBucket": dest,
		"destinationKey": key,
	})
	url := b.supabaseURL + "/storage/v1/object/copy"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.serviceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copy %d: %s", resp.StatusCode, truncate(string(msg), 200))
	}
	return nil
}

// StartDailyBackup ensures backup buckets exist, runs an initial mirror in
// the background and then repeats every 24h. A restore is a matter of
// copying from *-backup back to the source bucket via the same endpoint.
func (b *BackupService) StartDailyBackup(ctx context.Context) {
	go func() {
		if err := b.EnsureBackupBuckets(ctx); err != nil {
			log.Printf("[BACKUP] ensure buckets: %v", err)
		}
		if stats := b.MirrorAll(ctx); len(stats) > 0 {
			total := 0
			for _, s := range stats {
				total += s.Copied
			}
			log.Printf("[BACKUP] startup mirror complete, copied=%d", total)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.MirrorAll(ctx)
			}
		}
	}()
	log.Println("[SVC] Bucket backup mirror started (daily, incremental)")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
