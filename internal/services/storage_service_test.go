package services

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeSupabase reproduces enough of Supabase Storage's REST surface
// to cover the paths StorageService touches: bucket GET/POST and
// object POST. Each test wires a custom handler into it.
type fakeSupabase struct {
	mu *httptest.Server

	bucketGetCalls    int32
	bucketCreateCalls int32
	uploadCalls       int32

	// scripted responses
	bucketGetStatus    int
	bucketCreateStatus int
	uploadStatus       int
	uploadBody         string
}

func newFakeSupabase(t *testing.T, bGet, bCreate, up int, upBody string) *fakeSupabase {
	t.Helper()
	f := &fakeSupabase{
		bucketGetStatus:    bGet,
		bucketCreateStatus: bCreate,
		uploadStatus:       up,
		uploadBody:         upBody,
	}
	f.mu = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/storage/v1/bucket/"):
			atomic.AddInt32(&f.bucketGetCalls, 1)
			w.WriteHeader(f.bucketGetStatus)
		case r.Method == http.MethodPost && r.URL.Path == "/storage/v1/bucket":
			atomic.AddInt32(&f.bucketCreateCalls, 1)
			w.WriteHeader(f.bucketCreateStatus)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/storage/v1/object/"):
			atomic.AddInt32(&f.uploadCalls, 1)
			w.WriteHeader(f.uploadStatus)
			io.WriteString(w, f.uploadBody)
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(f.mu.Close)
	return f
}

// Happy path: bucket exists (200) and upload returns 200. URL must
// point to the public prefix.
func TestStorageUpload_BucketExists_UploadOK(t *testing.T) {
	f := newFakeSupabase(t, http.StatusOK, 0, http.StatusOK, "")
	svc := NewStorageService(f.mu.URL, "fake-key")

	url, err := svc.Upload(
		context.Background(), "promo-banners", "t1/a.png",
		[]byte("fake-png-bytes"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(url, "/storage/v1/object/public/promo-banners/t1/a.png") {
		t.Fatalf("unexpected url: %s", url)
	}
	if atomic.LoadInt32(&f.bucketCreateCalls) != 0 {
		t.Errorf("did not expect bucket create; got %d", f.bucketCreateCalls)
	}
	if atomic.LoadInt32(&f.uploadCalls) != 1 {
		t.Errorf("expected 1 upload call, got %d", f.uploadCalls)
	}
}

// Cold-start path: bucket GET returns 404 → we must POST /bucket and
// THEN upload. This is exactly the production bug that caused
// "no se pudo guardar el banner".
func TestStorageUpload_BucketMissing_AutoCreates(t *testing.T) {
	f := newFakeSupabase(t,
		http.StatusNotFound, // GET /bucket/:id
		http.StatusOK,       // POST /bucket
		http.StatusOK,       // POST /object
		"")
	svc := NewStorageService(f.mu.URL, "fake-key")

	_, err := svc.Upload(
		context.Background(), "promo-banners", "t1/a.png",
		[]byte("data"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&f.bucketCreateCalls) != 1 {
		t.Errorf("expected bucket create to be called once, got %d",
			f.bucketCreateCalls)
	}
}

// Concurrent-process path: GET returns 404 but someone else raced
// us and POST returns 409 Conflict — still success.
func TestStorageUpload_BucketConflict_TreatedAsSuccess(t *testing.T) {
	f := newFakeSupabase(t,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusOK, "")
	svc := NewStorageService(f.mu.URL, "fake-key")

	if _, err := svc.Upload(
		context.Background(), "promo-banners", "k", []byte("x"), "image/png",
	); err != nil {
		t.Fatalf("409 on bucket should not fail upload: %v", err)
	}
}

// The bug we're fixing today: Supabase returns 400 with a message
// body. We MUST propagate that body so the client sees the actual
// cause ("Bucket not found", "mime type not allowed", etc.) instead
// of a generic 500.
func TestStorageUpload_UploadFails_ErrorIsTransparent(t *testing.T) {
	body := `{"statusCode":"400","error":"Bucket not found","message":"Bucket not found"}`
	f := newFakeSupabase(t, http.StatusOK, 0, http.StatusBadRequest, body)
	svc := NewStorageService(f.mu.URL, "fake-key")

	_, err := svc.Upload(
		context.Background(), "promo-banners", "k",
		[]byte("x"), "image/png")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should include status code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Bucket not found") {
		t.Errorf("error should surface upstream message, got: %v", err)
	}
}

// Ensure idempotency: after a successful upload we don't re-check
// the bucket on subsequent uploads of the same bucket.
func TestStorageUpload_BucketEnsuredOnlyOnce(t *testing.T) {
	f := newFakeSupabase(t, http.StatusOK, 0, http.StatusOK, "")
	svc := NewStorageService(f.mu.URL, "fake-key")

	for i := 0; i < 3; i++ {
		if _, err := svc.Upload(
			context.Background(), "promo-banners", "k", []byte("x"), "image/png",
		); err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&f.bucketGetCalls); got != 1 {
		t.Errorf("expected 1 bucket GET total (memoised), got %d", got)
	}
}

// Empty body must be rejected locally — we never want a 0-byte
// object creating a ghost URL.
func TestStorageUpload_RejectsEmpty(t *testing.T) {
	f := newFakeSupabase(t, http.StatusOK, 0, http.StatusOK, "")
	svc := NewStorageService(f.mu.URL, "fake-key")

	_, err := svc.Upload(
		context.Background(), "promo-banners", "k", []byte{}, "image/png")
	if err == nil {
		t.Fatal("expected error on empty body")
	}
	if atomic.LoadInt32(&f.uploadCalls) != 0 {
		t.Errorf("should not have hit upstream; got %d calls",
			f.uploadCalls)
	}
}
