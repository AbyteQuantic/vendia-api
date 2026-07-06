// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestAnalyticsDashboard_ProductCountExcludesIncompleteMenuItems is the
// regression for the Dashboard "Inventario" KPI card disagreeing with
// "Mi Inventario" for the SAME sede (reporte 2026-07-06): a menu item
// (plato) without a costable recipe is not "sellable" — ListProducts
// with sellable_only=true already excludes it (Spec 078); the
// Dashboard's product_count must use the identical definition so both
// screens show the same number for the same branch.
func TestAnalyticsDashboard_ProductCountExcludesIncompleteMenuItems(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{}, &models.Recipe{}, &models.RecipeIngredient{},
		&models.Sale{}, &models.CreditAccount{}, &models.OrderTicket{}, &models.Branch{},
	))
	tenantID := "tenant-kpi"

	// Tienda product — always counts.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-tienda"},
		TenantID:  tenantID, Name: "Gaseosa", Price: 3000, IsAvailable: true,
	}).Error)

	// Complete menu item (recipe + ingredient) — counts.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "plato-completo"},
		TenantID:  tenantID, Name: "Sopa", Price: 12000, IsAvailable: true, IsMenuItem: true,
	}).Error)
	pid := "plato-completo"
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel: models.BaseModel{ID: "recipe-1"}, TenantID: tenantID, ProductID: &pid,
	}).Error)
	require.NoError(t, db.Create(&models.RecipeIngredient{
		BaseModel: models.BaseModel{ID: "ri-1"}, RecipeUUID: "recipe-1",
	}).Error)

	// Incomplete menu item (no recipe at all) — must NOT count.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "plato-incompleto"},
		TenantID:  tenantID, Name: "Bandeja", Price: 18000, IsAvailable: true, IsMenuItem: true,
	}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() })
	r.GET("/analytics/dashboard", handlers.AnalyticsDashboard(db))

	w := doJSON(t, r, http.MethodGet, "/analytics/dashboard", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			ProductCount int64 `json:"product_count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, int64(2), resp.Data.ProductCount,
		"solo el producto de tienda + el plato con receta completa cuentan")
}
