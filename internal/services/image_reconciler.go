package services

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// ImageReconciler scans products for image URLs pointing to storage and clears
// any URL whose file no longer exists (HTTP 404). This is a self-healing safety
// net: even if some future code path or human action deletes a bucket object,
// the merchant sees a clean "needs photo" state instead of a broken image —
// and it makes image-loss regressions loud and observable in logs.
type ImageReconciler struct {
	db      *gorm.DB
	client  *http.Client
	baseURL string // e.g. https://<project>.supabase.co/storage/v1/object/public/
}

func NewImageReconciler(db *gorm.DB, supabaseURL string) *ImageReconciler {
	base := ""
	if supabaseURL != "" {
		base = strings.TrimRight(supabaseURL, "/") + "/storage/v1/object/public/"
	}
	return &ImageReconciler{
		db:      db,
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: base,
	}
}

// ReconcileStats summarizes a reconciliation pass.
type ReconcileStats struct {
	Scanned int
	Broken  int
	Cleared int
}

// ReconcileProductImages scans products.photo_url and products.image_url. Any
// URL that belongs to our Supabase storage host and returns 404 gets cleared
// in the database so the UI falls back to the "generate photo" affordance
// instead of rendering a broken image. URLs that are reachable or that point
// to external hosts (OpenFoodFacts, etc.) are left untouched.
func (r *ImageReconciler) ReconcileProductImages(ctx context.Context) (ReconcileStats, error) {
	stats := ReconcileStats{}
	if r.baseURL == "" {
		return stats, nil // supabase not configured, nothing to reconcile
	}

	type productRow struct {
		ID       string
		PhotoURL string
		ImageURL string
	}
	var rows []productRow
	if err := r.db.WithContext(ctx).
		Table("products").
		Select("id, photo_url, image_url").
		Where("(photo_url LIKE ? OR image_url LIKE ?)", r.baseURL+"%", r.baseURL+"%").
		Find(&rows).Error; err != nil {
		return stats, fmt.Errorf("load product image urls: %w", err)
	}
	stats.Scanned = len(rows)
	if stats.Scanned == 0 {
		return stats, nil
	}

	// Probe a unique set of URLs with small concurrency to avoid slamming the
	// storage endpoint on large inventories.
	urls := make(map[string]struct{}, 2*len(rows))
	for _, p := range rows {
		if strings.HasPrefix(p.PhotoURL, r.baseURL) {
			urls[p.PhotoURL] = struct{}{}
		}
		if strings.HasPrefix(p.ImageURL, r.baseURL) {
			urls[p.ImageURL] = struct{}{}
		}
	}
	broken := r.findBroken(ctx, urls, 4)
	stats.Broken = len(broken)
	if stats.Broken == 0 {
		return stats, nil
	}

	brokenList := make([]string, 0, len(broken))
	for u := range broken {
		brokenList = append(brokenList, u)
	}

	// Clear in a single pass per column so we don't drop an OFF image_url that
	// happens to live alongside a broken photo_url.
	res := r.db.WithContext(ctx).Table("products").
		Where("photo_url IN ?", brokenList).
		Update("photo_url", "")
	stats.Cleared += int(res.RowsAffected)

	res = r.db.WithContext(ctx).Table("products").
		Where("image_url IN ?", brokenList).
		Update("image_url", "")
	stats.Cleared += int(res.RowsAffected)

	log.Printf("[RECONCILE] scanned=%d broken=%d cleared_columns=%d sample=%q",
		stats.Scanned, stats.Broken, stats.Cleared, sample(brokenList))
	return stats, nil
}

// findBroken returns the subset of URLs that respond with 404 Not Found.
// Network errors and other statuses are treated as "maybe ok" and skipped so
// a transient outage cannot wipe merchant data.
func (r *ImageReconciler) findBroken(ctx context.Context, urls map[string]struct{}, concurrency int) map[string]struct{} {
	broken := make(map[string]struct{})
	var mu sync.Mutex

	type job struct{ url string }
	ch := make(chan job)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				req, err := http.NewRequestWithContext(ctx, http.MethodHead, j.url, nil)
				if err != nil {
					continue
				}
				resp, err := r.client.Do(req)
				if err != nil {
					continue
				}
				resp.Body.Close()
				if resp.StatusCode == http.StatusNotFound {
					mu.Lock()
					broken[j.url] = struct{}{}
					mu.Unlock()
				}
			}
		}()
	}
	for u := range urls {
		ch <- job{url: u}
	}
	close(ch)
	wg.Wait()
	return broken
}

func sample(s []string) string {
	if len(s) == 0 {
		return ""
	}
	if len(s) > 3 {
		return strings.Join(s[:3], " | ") + " ..."
	}
	return strings.Join(s, " | ")
}

// StartDailyReconcile runs reconciliation immediately (non-blocking) and then
// every 24h. Daily cadence catches any future image loss within a day.
func (r *ImageReconciler) StartDailyReconcile(ctx context.Context) {
	go func() {
		// Initial pass — boot-time self-heal for anything already broken.
		if stats, err := r.ReconcileProductImages(ctx); err != nil {
			log.Printf("[RECONCILE] startup error: %v", err)
		} else if stats.Scanned > 0 {
			log.Printf("[RECONCILE] startup: scanned=%d broken=%d cleared=%d",
				stats.Scanned, stats.Broken, stats.Cleared)
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := r.ReconcileProductImages(ctx); err != nil {
					log.Printf("[RECONCILE] daily error: %v", err)
				}
			}
		}
	}()
	log.Println("[SVC] Product image reconciler started (daily self-heal)")
}
