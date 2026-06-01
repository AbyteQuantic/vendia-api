// Spec: hotfix 2026-05-31 admin /analytics
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAnalyticsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{},
		&models.Sale{},
		&models.SaleItem{},
	))
	return db
}

func mountAnalyticsRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/admin/analytics", handlers.AdminAnalytics(db))
	return r
}

// TestAdminAnalytics_EmptyDB — sin datos, devuelve las 6 secciones
// vacías (no nil, no error). Cubre el caso "tenant nuevo".
func TestAdminAnalytics_EmptyDB(t *testing.T) {
	db := setupAnalyticsDB(t)
	r := mountAnalyticsRouter(db)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/analytics?days=7", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// 7 días → 7 puntos en sales_trend (zero-fill).
	trend, _ := resp["sales_trend"].([]any)
	assert.Len(t, trend, 7)
	// Resto secciones vacías pero presentes (no nil).
	assert.NotNil(t, resp["payment_methods"])
	assert.NotNil(t, resp["sales_by_business_type"])
	assert.NotNil(t, resp["top_products"])
	assert.NotNil(t, resp["activity_heatmap"])
	oo := resp["online_vs_offline"].(map[string]any)
	assert.EqualValues(t, 0, oo["online"])
	assert.EqualValues(t, 0, oo["offline"])
}

// TestAdminAnalytics_SalesTrend — agrupa ventas por día UTC y
// zero-fill. Usa timestamps anclados al MEDIODÍA UTC para evitar
// off-by-one en el rollover de medianoche (si el test corre justo
// a las 23:50 UTC, "hace 1 hora" cae en el mismo día UTC).
func TestAdminAnalytics_SalesTrend(t *testing.T) {
	db := setupAnalyticsDB(t)
	now := time.Now().UTC()
	noonToday := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)

	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{CreatedAt: noonToday},
		TenantID:  "t1", Total: 5000,
	}).Error)
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{CreatedAt: noonToday.Add(-1 * time.Hour)},
		TenantID:  "t1", Total: 3000,
	}).Error)
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{CreatedAt: noonToday.AddDate(0, 0, -2)},
		TenantID:  "t1", Total: 7000,
	}).Error)

	r := mountAnalyticsRouter(db)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/analytics?days=7", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	trend := resp["sales_trend"].([]any)
	require.Len(t, trend, 7, "7 días zero-fill")

	today := trend[6].(map[string]any)
	assert.EqualValues(t, 8000, today["total"])
	assert.EqualValues(t, 2, today["transactions"])
	yesterday := trend[5].(map[string]any)
	assert.EqualValues(t, 0, yesterday["total"])
	twoDaysAgo := trend[4].(map[string]any)
	assert.EqualValues(t, 7000, twoDaysAgo["total"])
}

// TestAdminAnalytics_PaymentMethods — agrupa por payment_method.
func TestAdminAnalytics_PaymentMethods(t *testing.T) {
	db := setupAnalyticsDB(t)
	now := time.Now().UTC()

	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{CreatedAt: now},
		TenantID:  "t1", Total: 5000, PaymentMethod: models.PaymentCash,
	}).Error)
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{CreatedAt: now},
		TenantID:  "t1", Total: 3000, PaymentMethod: models.PaymentCash,
	}).Error)
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{CreatedAt: now},
		TenantID:  "t1", Total: 10000, PaymentMethod: models.PaymentTransfer,
	}).Error)

	r := mountAnalyticsRouter(db)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/analytics?days=7", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	pm := resp["payment_methods"].([]any)
	require.Len(t, pm, 2)
	// Transfer aparece primero porque sortea por total desc.
	first := pm[0].(map[string]any)
	assert.Equal(t, "transfer", first["method"])
	assert.EqualValues(t, 10000, first["total"])
}

// TestAdminAnalytics_DaysParam — clampea entre 1 y 90.
func TestAdminAnalytics_DaysParam(t *testing.T) {
	cases := []struct {
		input    string
		expected int
	}{
		{"", 7},        // default
		{"0", 7},       // ≤0 → default
		{"abc", 7},     // inválido → default
		{"30", 30},
		{"90", 90},
		{"500", 90},    // clamp
	}
	for _, c := range cases {
		t.Run("days="+c.input, func(t *testing.T) {
			db := setupAnalyticsDB(t)
			r := mountAnalyticsRouter(db)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
				"/api/v1/admin/analytics?days="+c.input, nil))
			require.Equal(t, http.StatusOK, w.Code)
			var resp map[string]any
			json.Unmarshal(w.Body.Bytes(), &resp)
			trend := resp["sales_trend"].([]any)
			assert.Len(t, trend, c.expected)
		})
	}
}
