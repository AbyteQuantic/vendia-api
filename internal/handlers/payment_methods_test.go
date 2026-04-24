package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeStorage is an in-memory FileStorage double so QR upload tests
// don't need a live Supabase/R2 bucket. It records every Upload call
// and can be forced into failure to exercise the error branch.
type fakeStorage struct {
	uploads     map[string][]byte
	forceErr    error
	lastBucket  string
	lastKey     string
	lastContent string
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{uploads: map[string][]byte{}}
}

func (f *fakeStorage) Upload(_ context.Context, bucket, key string, data []byte, contentType string) (string, error) {
	if f.forceErr != nil {
		return "", f.forceErr
	}
	f.lastBucket = bucket
	f.lastKey = key
	f.lastContent = contentType
	f.uploads[bucket+"/"+key] = append([]byte(nil), data...)
	return fmt.Sprintf("https://fake-cdn.example/%s/%s", bucket, key), nil
}

func (f *fakeStorage) Download(_ context.Context, _, _ string) ([]byte, string, error) {
	return nil, "", nil
}

func (f *fakeStorage) Delete(_ context.Context, _, _ string) error {
	return nil
}

func setupPaymentMethodsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.TenantPaymentMethod{}))
	return db
}

// buildMultipartQR hand-rolls a multipart body with a single `qr`
// file field so we don't depend on httptest's multipart helpers.
func buildMultipartQR(t *testing.T, fieldName, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	hdr := make(map[string][]string)
	hdr["Content-Disposition"] = []string{
		fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, filename),
	}
	hdr["Content-Type"] = []string{contentType}
	part, err := mw.CreatePart(hdr)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return body, mw.FormDataContentType()
}

func TestUploadPaymentMethodQR_HappyPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupPaymentMethodsTestDB(t)
	storage := newFakeStorage()

	tenantID := "tenant-1"
	pm := models.TenantPaymentMethod{
		BaseModel:      models.BaseModel{ID: "pm-1"},
		TenantID:       tenantID,
		Name:           "Nequi",
		AccountDetails: "3001234567",
		Provider:       "nequi",
		IsActive:       true,
	}
	require.NoError(t, db.Create(&pm).Error)

	r := gin.New()
	r.POST("/payment-methods/:id/qr", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		UploadPaymentMethodQR(db, storage)(c)
	})

	body, ctype := buildMultipartQR(
		t, "qr", "nequi.png", "image/png",
		[]byte("\x89PNG\r\n\x1a\nfake-png-bytes"))

	req := httptest.NewRequest(http.MethodPost, "/payment-methods/pm-1/qr", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data struct {
			ID         string `json:"id"`
			QRImageURL string `json:"qr_image_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "pm-1", resp.Data.ID)
	assert.Contains(t, resp.Data.QRImageURL, "payment-qrs/")
	assert.True(t, strings.HasPrefix(resp.Data.QRImageURL, "https://"),
		"QR URL must be absolute, got %q", resp.Data.QRImageURL)

	// Storage side-effects
	assert.Equal(t, "payment-qrs", storage.lastBucket)
	assert.Equal(t, "image/png", storage.lastContent)
	assert.True(t, strings.HasPrefix(storage.lastKey, tenantID+"/pm-1-"),
		"key should be tenant-scoped and include method id; got %q", storage.lastKey)

	// DB persisted the new URL.
	var reloaded models.TenantPaymentMethod
	require.NoError(t, db.First(&reloaded, "id = ?", "pm-1").Error)
	assert.Equal(t, resp.Data.QRImageURL, reloaded.QRImageURL)
}

func TestUploadPaymentMethodQR_CrossTenantIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupPaymentMethodsTestDB(t)
	storage := newFakeStorage()

	// Method belongs to tenant A but the session claims tenant B.
	require.NoError(t, db.Create(&models.TenantPaymentMethod{
		BaseModel: models.BaseModel{ID: "pm-a"},
		TenantID:  "tenant-A",
		Name:      "Nequi",
		IsActive:  true,
	}).Error)

	r := gin.New()
	r.POST("/payment-methods/:id/qr", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, "tenant-B")
		UploadPaymentMethodQR(db, storage)(c)
	})

	body, ctype := buildMultipartQR(
		t, "qr", "x.png", "image/png", []byte("bytes"))
	req := httptest.NewRequest(http.MethodPost, "/payment-methods/pm-a/qr", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code,
		"tenant B must not be able to touch tenant A's method")
	assert.Empty(t, storage.uploads,
		"no storage write should happen when the auth check fails")
}

func TestUploadPaymentMethodQR_RejectsNonImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupPaymentMethodsTestDB(t)
	storage := newFakeStorage()

	tenantID := "tenant-1"
	require.NoError(t, db.Create(&models.TenantPaymentMethod{
		BaseModel: models.BaseModel{ID: "pm-1"},
		TenantID:  tenantID, Name: "Nequi", IsActive: true,
	}).Error)

	r := gin.New()
	r.POST("/payment-methods/:id/qr", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		UploadPaymentMethodQR(db, storage)(c)
	})

	body, ctype := buildMultipartQR(
		t, "qr", "notes.txt", "text/plain", []byte("hello"))
	req := httptest.NewRequest(http.MethodPost, "/payment-methods/pm-1/qr", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, storage.uploads)
}

func TestUploadPaymentMethodQR_SurfacesStorageFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupPaymentMethodsTestDB(t)
	storage := newFakeStorage()
	storage.forceErr = fmt.Errorf(
		"storage upload returned 400: {\"error\":\"Bucket not found\"}")

	tenantID := "tenant-1"
	require.NoError(t, db.Create(&models.TenantPaymentMethod{
		BaseModel: models.BaseModel{ID: "pm-1"},
		TenantID:  tenantID, Name: "Nequi", IsActive: true,
	}).Error)

	r := gin.New()
	r.POST("/payment-methods/:id/qr", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		UploadPaymentMethodQR(db, storage)(c)
	})

	body, ctype := buildMultipartQR(
		t, "qr", "x.png", "image/png", []byte("bytes"))
	req := httptest.NewRequest(http.MethodPost, "/payment-methods/pm-1/qr", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "no se pudo subir el QR", resp["error"])
	// Critical: detail must carry the raw storage error so the
	// tendero's toast doesn't hide it (same contract as promotions).
	detail, _ := resp["detail"].(string)
	assert.Contains(t, detail, "Bucket not found")
}

func TestNormalizeProviderFromName(t *testing.T) {
	cases := []struct {
		name, want string
	}{
		{"Nequi", "nequi"},
		{"NEQUI 3001234567", "nequi"},
		{"Daviplata", "daviplata"},
		{"Bancolombia a la Mano", "bancolombia"},
		{"Davivienda", "davivienda"},
		{"Breve", "breve"},
		{"Efectivo", "efectivo"},
		{"Tarjeta VISA", "tarjeta"},
		{"Mi Criptomoneda", "otro"},
		{"", "otro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeProviderFromName(tc.name))
		})
	}
}
