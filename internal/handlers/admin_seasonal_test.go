// Spec: specs/086-branding-estacional/spec.md
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func mountAdminSeasonal(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/seasonal-campaigns", handlers.AdminListSeasonalCampaigns(db))
	r.POST("/admin/seasonal-campaigns", handlers.AdminCreateSeasonalCampaign(db))
	r.PATCH("/admin/seasonal-campaigns/:id", handlers.AdminUpdateSeasonalCampaign(db))
	r.POST("/admin/seasonal-campaigns/:id/activate", handlers.AdminActivateSeasonalCampaign(db))
	return r
}

func adminSeasonalDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.SeasonalCampaign{}))
	return db
}

func TestAdminSeasonal_CreateValidatesAndPersists(t *testing.T) {
	db := adminSeasonalDB(t)
	r := mountAdminSeasonal(db)

	// Válida → 201.
	ok := doJSON(t, r, http.MethodPost, "/admin/seasonal-campaigns", map[string]any{
		"key": "navidad_2026", "name": "Navidad 2026", "enabled": true,
		"accent_hex": "#C0392B", "icon_variant": "navidad",
	})
	require.Equal(t, http.StatusCreated, ok.Code, ok.Body.String())

	// Hex inválido → 422.
	badHex := doJSON(t, r, http.MethodPost, "/admin/seasonal-campaigns", map[string]any{
		"key": "x_test", "accent_hex": "rojo",
	})
	assert.Equal(t, http.StatusUnprocessableEntity, badHex.Code)

	// Key inválida → 422.
	badKey := doJSON(t, r, http.MethodPost, "/admin/seasonal-campaigns", map[string]any{
		"key": "Navidad 2026!",
	})
	assert.Equal(t, http.StatusUnprocessableEntity, badKey.Code)

	// icon_variant fuera de whitelist → 422.
	badIcon := doJSON(t, r, http.MethodPost, "/admin/seasonal-campaigns", map[string]any{
		"key": "y_test", "icon_variant": "hackeo",
	})
	assert.Equal(t, http.StatusUnprocessableEntity, badIcon.Code)

	// key duplicada → 409.
	dup := doJSON(t, r, http.MethodPost, "/admin/seasonal-campaigns", map[string]any{
		"key": "navidad_2026",
	})
	assert.Equal(t, http.StatusConflict, dup.Code)

	// List → al menos 1.
	g := httptest.NewRequest(http.MethodGet, "/admin/seasonal-campaigns", nil)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, g)
	require.Equal(t, http.StatusOK, gw.Code)
	assert.Contains(t, gw.Body.String(), "navidad_2026")
}

func TestAdminSeasonal_Activate(t *testing.T) {
	db := adminSeasonalDB(t)
	camp := models.SeasonalCampaign{Key: "k", Name: "K", IconVariant: "default"}
	require.NoError(t, db.Create(&camp).Error)
	r := mountAdminSeasonal(db)
	w := doJSON(t, r, http.MethodPost, "/admin/seasonal-campaigns/"+camp.ID+"/activate",
		map[string]any{"enabled": true, "force_active": true})
	require.Equal(t, http.StatusOK, w.Code)
	var got models.SeasonalCampaign
	db.First(&got, "id = ?", camp.ID)
	assert.True(t, got.Enabled)
	assert.True(t, got.ForceActive)
}
