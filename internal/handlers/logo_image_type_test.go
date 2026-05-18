// Spec: specs/010-logo-heic-iphone/spec.md
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// --- detectImageType ----------------------------------------------------

// jpegHeader / pngHeader / etc. are the minimal magic-number prefixes a
// real file of each format always starts with. detectImageType only ever
// reads the first bytes, so a prefix is enough to exercise the sniff.
var (
	jpegHeader = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	pngHeader  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	gifHeader  = []byte("GIF89a")
)

// webpHeader is "RIFF" + 4-byte size + "WEBP".
func webpHeader() []byte {
	b := make([]byte, 16)
	copy(b[0:4], "RIFF")
	copy(b[8:12], "WEBP")
	return b
}

// heicHeader builds a synthetic HEIC/HEIF header: a 4-byte box size,
// the literal "ftyp" at offset 4, and the brand at offset 8.
func heicHeader(brand string) []byte {
	b := make([]byte, 32)
	b[0], b[1], b[2], b[3] = 0x00, 0x00, 0x00, 0x18 // box size
	copy(b[4:8], "ftyp")
	copy(b[8:12], brand)
	return b
}

func TestDetectImageType(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", jpegHeader, "image/jpeg"},
		{"png", pngHeader, "image/png"},
		{"gif87a", []byte("GIF87a"), "image/gif"},
		{"gif89a", gifHeader, "image/gif"},
		{"webp", webpHeader(), "image/webp"},
		{"heic brand heic", heicHeader("heic"), "image/heic"},
		{"heic brand heix", heicHeader("heix"), "image/heic"},
		{"heic brand hevc", heicHeader("hevc"), "image/heic"},
		{"heic brand hevx", heicHeader("hevx"), "image/heic"},
		{"heic brand mif1", heicHeader("mif1"), "image/heic"},
		{"heif brand heif", heicHeader("heif"), "image/heic"},
		{"heif brand msf1", heicHeader("msf1"), "image/heic"},
		{"unknown empty", []byte{}, ""},
		{"unknown too short", []byte{0xFF}, ""},
		{"unknown garbage", []byte("not an image at all really"), ""},
		{"riff but not webp", append([]byte("RIFF1234"), []byte("WAVE")...), ""},
		{"ftyp but unknown brand", heicHeader("qt  "), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectImageType(tt.data); got != tt.want {
				t.Errorf("detectImageType(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// --- PreviewLogoUpload --------------------------------------------------

// captureStorage records the contentType passed to Upload so the test
// can assert the handler uploads with the *detected* type, not the
// client-supplied Content-Type.
type captureStorage struct {
	gotContentType string
	gotData        []byte
	uploadCalled   bool
	uploadErr      error
}

func (s *captureStorage) Upload(_ context.Context, _, key string, data []byte, contentType string) (string, error) {
	s.uploadCalled = true
	s.gotContentType = contentType
	s.gotData = data
	if s.uploadErr != nil {
		return "", s.uploadErr
	}
	return "https://cdn.vendia.store/" + key, nil
}

func (s *captureStorage) Download(_ context.Context, _, _ string) ([]byte, string, error) {
	return nil, "", nil
}

func (s *captureStorage) Delete(_ context.Context, _, _ string) error { return nil }

// buildLogoForm assembles a multipart/form-data request body with one
// "logo" file part. clientType is the Content-Type the client claims
// for that part (the iPhone bug ships HEIC bytes as image/heic).
func buildLogoForm(t *testing.T, filename, clientType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{
		`form-data; name="logo"; filename="` + filename + `"`,
	}
	h["Content-Type"] = []string{clientType}
	part, err := w.CreatePart(h)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("part.Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	return body, w.FormDataContentType()
}

func TestPreviewLogoUpload_HeicReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	storage := &captureStorage{}

	// HEIC bytes, but the client lies and could even claim image/jpeg —
	// the backend must sniff the real bytes regardless.
	body, ctype := buildLogoForm(t, "IMG_0420.heic", "image/heic", heicHeader("heic"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/preview-logo-upload", body)
	c.Request.Header.Set("Content-Type", ctype)

	PreviewLogoUpload(storage)(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for HEIC upload, got %d (body: %s)", w.Code, w.Body.String())
	}
	if storage.uploadCalled {
		t.Error("storage.Upload must NOT be called for an unsupported format")
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp["error_code"] != "logo_formato_no_soportado" {
		t.Errorf("expected error_code=logo_formato_no_soportado, got %v", resp["error_code"])
	}
	if msg, _ := resp["error"].(string); msg == "" {
		t.Error("expected a non-empty Spanish error message")
	}
}

func TestPreviewLogoUpload_JpegUsesDetectedType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	storage := &captureStorage{}

	// Client lies: ships JPEG bytes but claims image/heic. The backend
	// must upload with the DETECTED image/jpeg, not the bogus header.
	body, ctype := buildLogoForm(t, "logo.jpg", "image/heic", jpegHeader)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/preview-logo-upload", body)
	c.Request.Header.Set("Content-Type", ctype)

	PreviewLogoUpload(storage)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for JPEG upload, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !storage.uploadCalled {
		t.Fatal("storage.Upload was not called for a valid JPEG")
	}
	if storage.gotContentType != "image/jpeg" {
		t.Errorf("expected upload content-type image/jpeg (detected), got %q", storage.gotContentType)
	}
}

func TestPreviewLogoUpload_UnknownFormatReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	storage := &captureStorage{}

	body, ctype := buildLogoForm(t, "weird.dat", "image/png", []byte("totally not an image"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/preview-logo-upload", body)
	c.Request.Header.Set("Content-Type", ctype)

	PreviewLogoUpload(storage)(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown format, got %d (body: %s)", w.Code, w.Body.String())
	}
	if storage.uploadCalled {
		t.Error("storage.Upload must NOT be called for an unknown format")
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error_code"] != "logo_formato_no_soportado" {
		t.Errorf("expected error_code=logo_formato_no_soportado, got %v", resp["error_code"])
	}
}
