// Spec: specs/086-branding-estacional/spec.md
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupBrandingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.SeasonalCampaign{}))
	return db
}

func mountBranding(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/branding/season", handlers.GetSeasonalBranding(db))
	return r
}

func get(r *gin.Engine, ifNoneMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/branding/season", nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func acc(s string) *string { return &s }

// Sin temporada → 200 {active:false}, NUNCA 500 (fail-closed).
func TestBranding_NoSeason(t *testing.T) {
	r := mountBranding(setupBrandingDB(t))
	w := get(r, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"active":false`)
	assert.NotEmpty(t, w.Header().Get("ETag"))
	// 304 también funciona fuera de temporada.
	w2 := get(r, w.Header().Get("ETag"))
	assert.Equal(t, http.StatusNotModified, w2.Code)
}

// Campaña force_active → 200 con overrides + ETag; If-None-Match → 304.
func TestBranding_ActiveAndETag(t *testing.T) {
	db := setupBrandingDB(t)
	require.NoError(t, db.Create(&models.SeasonalCampaign{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		Key:       "navidad_2026", Name: "Navidad 2026",
		Enabled: true, ForceActive: true, IconVariant: "navidad",
		AccentHex: acc("#C0392B"), SplashMessage: acc("Feliz Navidad"),
	}).Error)
	r := mountBranding(db)

	w := get(r, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, `"active":true`)
	assert.Contains(t, body, `"key":"navidad_2026"`)
	assert.Contains(t, body, `"accent_hex":"#C0392B"`)
	assert.Contains(t, body, `"icon_variant":"navidad"`)
	assert.Contains(t, body, `"message":"Feliz Navidad"`)

	etag := w.Header().Get("ETag")
	require.NotEmpty(t, etag)
	w2 := get(r, etag)
	assert.Equal(t, http.StatusNotModified, w2.Code)
}

// enabled=false → no se sirve (active:false).
func TestBranding_DisabledIgnored(t *testing.T) {
	db := setupBrandingDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&models.SeasonalCampaign{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		Key:       "off", Enabled: false, ForceActive: true,
		StartsAt: &now,
	}).Error)
	w := get(mountBranding(db), "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"active":false`)
}
