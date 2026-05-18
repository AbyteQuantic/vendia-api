// Spec: specs/019-foto-perfil-tendero-empleado/spec.md
package handlers

import (
	"encoding/json"
	"fmt"
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

// setupEmployeePhotoDB opens an in-memory sqlite DB with just the
// Employee table — the only schema UploadEmployeePhoto touches.
func setupEmployeePhotoDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Employee{}))
	return db
}

// pngBytes is a minimal byte slice whose magic number detectImageType
// recognises as image/png — enough to pass the format gate without a
// real image encoder.
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	'f', 'a', 'k', 'e', '-', 'p', 'n', 'g',
}

// TestUploadEmployeePhoto_HappyPath — AC-01 / AC-02: a photo uploaded
// for an employee (or owner) is stored and its public URL persisted on
// the Employee row, and echoed back in the response.
func TestUploadEmployeePhoto_HappyPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEmployeePhotoDB(t)
	storage := newFakeStorage()

	tenantID := "tenant-1"
	require.NoError(t, db.Create(&models.Employee{
		BaseModel: models.BaseModel{ID: "emp-1"},
		TenantID:  tenantID,
		Name:      "María",
		Role:      models.RoleCashier,
		IsActive:  true,
	}).Error)

	r := gin.New()
	r.POST("/employees/:uuid/photo", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		UploadEmployeePhoto(db, storage)(c)
	})

	body, ctype := buildMultipartQR(
		t, "photo", "selfie.png", "image/png", pngBytes)
	req := httptest.NewRequest(http.MethodPost, "/employees/emp-1/photo", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data struct {
			ID       string `json:"id"`
			PhotoURL string `json:"photo_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "emp-1", resp.Data.ID)
	assert.True(t, strings.HasPrefix(resp.Data.PhotoURL, "https://"),
		"photo URL must be absolute, got %q", resp.Data.PhotoURL)

	// Storage side-effects: tenant-scoped key in the profile bucket.
	assert.Equal(t, "profile-photos", storage.lastBucket)
	assert.True(t, strings.HasPrefix(storage.lastKey, tenantID+"/emp-1-"),
		"key should be tenant-scoped and include employee id; got %q", storage.lastKey)

	// DB persisted the new URL on the employee row.
	var reloaded models.Employee
	require.NoError(t, db.First(&reloaded, "id = ?", "emp-1").Error)
	assert.Equal(t, resp.Data.PhotoURL, reloaded.PhotoURL)
}

// TestUploadEmployeePhoto_CrossTenantIsolation — Constitution Art. III:
// a session for tenant B must not be able to set a photo on tenant A's
// employee, and no storage write happens when the lookup fails.
func TestUploadEmployeePhoto_CrossTenantIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEmployeePhotoDB(t)
	storage := newFakeStorage()

	require.NoError(t, db.Create(&models.Employee{
		BaseModel: models.BaseModel{ID: "emp-a"},
		TenantID:  "tenant-A",
		Name:      "Empleado A",
		Role:      models.RoleCashier,
		IsActive:  true,
	}).Error)

	r := gin.New()
	r.POST("/employees/:uuid/photo", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, "tenant-B")
		UploadEmployeePhoto(db, storage)(c)
	})

	body, ctype := buildMultipartQR(
		t, "photo", "x.png", "image/png", pngBytes)
	req := httptest.NewRequest(http.MethodPost, "/employees/emp-a/photo", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code,
		"tenant B must not be able to touch tenant A's employee")
	assert.Empty(t, storage.uploads,
		"no storage write should happen when the auth check fails")

	// Tenant A's row stays untouched.
	var reloaded models.Employee
	require.NoError(t, db.First(&reloaded, "id = ?", "emp-a").Error)
	assert.Empty(t, reloaded.PhotoURL)
}

// TestUploadEmployeePhoto_RejectsUnsupportedFormat — F010 reuse: HEIC
// (and any non jpeg/png/webp/gif) is rejected at the boundary with a
// 400 in Spanish, never forwarded to storage.
func TestUploadEmployeePhoto_RejectsUnsupportedFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEmployeePhotoDB(t)
	storage := newFakeStorage()

	tenantID := "tenant-1"
	require.NoError(t, db.Create(&models.Employee{
		BaseModel: models.BaseModel{ID: "emp-1"},
		TenantID:  tenantID, Name: "María",
		Role: models.RoleCashier, IsActive: true,
	}).Error)

	r := gin.New()
	r.POST("/employees/:uuid/photo", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		UploadEmployeePhoto(db, storage)(c)
	})

	// "ftyp" + heic brand → detectImageType returns image/heic.
	heicBytes := []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c', 0x00, 0x00, 0x00, 0x00,
	}
	body, ctype := buildMultipartQR(
		t, "photo", "iphone.heic", "image/heic", heicBytes)
	req := httptest.NewRequest(http.MethodPost, "/employees/emp-1/photo", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, storage.uploads)
}

// TestUploadEmployeePhoto_MissingFile — no `photo` field → 400.
func TestUploadEmployeePhoto_MissingFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEmployeePhotoDB(t)
	storage := newFakeStorage()

	tenantID := "tenant-1"
	require.NoError(t, db.Create(&models.Employee{
		BaseModel: models.BaseModel{ID: "emp-1"},
		TenantID:  tenantID, Name: "María",
		Role: models.RoleCashier, IsActive: true,
	}).Error)

	r := gin.New()
	r.POST("/employees/:uuid/photo", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		UploadEmployeePhoto(db, storage)(c)
	})

	// Body carries a `wrongfield` part instead of `photo`.
	body, ctype := buildMultipartQR(
		t, "wrongfield", "x.png", "image/png", pngBytes)
	req := httptest.NewRequest(http.MethodPost, "/employees/emp-1/photo", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, storage.uploads)
}

// TestUploadEmployeePhoto_SurfacesStorageFailure — a storage error
// returns 500 with the raw upstream detail, mirroring the QR contract.
func TestUploadEmployeePhoto_SurfacesStorageFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEmployeePhotoDB(t)
	storage := newFakeStorage()
	storage.forceErr = fmt.Errorf(
		"storage upload returned 400: {\"error\":\"Bucket not found\"}")

	tenantID := "tenant-1"
	require.NoError(t, db.Create(&models.Employee{
		BaseModel: models.BaseModel{ID: "emp-1"},
		TenantID:  tenantID, Name: "María",
		Role: models.RoleCashier, IsActive: true,
	}).Error)

	r := gin.New()
	r.POST("/employees/:uuid/photo", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		UploadEmployeePhoto(db, storage)(c)
	})

	body, ctype := buildMultipartQR(
		t, "photo", "x.png", "image/png", pngBytes)
	req := httptest.NewRequest(http.MethodPost, "/employees/emp-1/photo", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	detail, _ := resp["detail"].(string)
	assert.Contains(t, detail, "Bucket not found")
}

// TestUploadEmployeePhoto_NilStorage — no storage configured → 503.
func TestUploadEmployeePhoto_NilStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEmployeePhotoDB(t)

	r := gin.New()
	r.POST("/employees/:uuid/photo", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, "tenant-1")
		UploadEmployeePhoto(db, nil)(c)
	})

	body, ctype := buildMultipartQR(
		t, "photo", "x.png", "image/png", pngBytes)
	req := httptest.NewRequest(http.MethodPost, "/employees/emp-1/photo", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
