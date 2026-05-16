// Spec: specs/001-insumos-recetas/spec.md
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

func setupRecipeCostDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Ingredient{},
		&models.Product{},
	))
	return db
}

func mountRecipeCostHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/recipes/:uuid/cost", handlers.RecipeCost(db))
	r.GET("/recipes/:uuid/availability", handlers.RecipeAvailability(db))
	return r
}

// seedAlmuerzoForCost wires "Almuerzo corriente" = 0.15 kg arroz +
// 0.2 kg pollo, with the given insumo stocks.
func seedAlmuerzoForCost(t *testing.T, db *gorm.DB, tenantID string, arrozStock, polloStock float64) string {
	t.Helper()
	recipeID := "b1000000-0000-4000-8000-000000000030"
	arrozID := "c1000000-0000-4000-8000-000000000030"
	polloID := "c2000000-0000-4000-8000-000000000030"
	aid, pid := arrozID, polloID

	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: arrozID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg,
		Stock: arrozStock, UnitCost: 2900,
	}).Error)
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: polloID},
		TenantID:  tenantID, Name: "Pollo", Unit: models.UnitKg,
		Stock: polloStock, UnitCost: 12000,
	}).Error)
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Almuerzo corriente",
		SalePrice:   12000,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: recipeID, ProductName: "Arroz", Quantity: 0.15, UnitCost: 2900, IngredientID: &aid},
			{RecipeUUID: recipeID, ProductName: "Pollo", Quantity: 0.2, UnitCost: 12000, IngredientID: &pid},
		},
	}).Error)
	return recipeID
}

// AC-02 — recipe cost rolls up Σ(insumo.unit_cost × line.quantity) with
// profit and margin. Arroz 0.15*2900 = 435, Pollo 0.2*12000 = 2400 →
// total 2835, sale 12000 → profit 9165.
func TestRecipeCost_RollsUpFromIngredients(t *testing.T) {
	db := setupRecipeCostDB(t)
	tenantID := "tenant-cost"
	recipeID := seedAlmuerzoForCost(t, db, tenantID, 10, 10)

	r := mountRecipeCostHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/recipes/"+recipeID+"/cost", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			TotalCost     float64 `json:"total_cost"`
			Profit        float64 `json:"profit"`
			MarginPercent float64 `json:"margin_percent"`
			SalePrice     float64 `json:"sale_price"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.InDelta(t, 2835, resp.Data.TotalCost, 1e-6, "0.15*2900 + 0.2*12000")
	assert.InDelta(t, 9165, resp.Data.Profit, 1e-6, "12000 - 2835")
	assert.Greater(t, resp.Data.MarginPercent, float64(0))
}

// AC-03 — availability = min over insumos of floor(stock/qty).
// Arroz 3/0.15 = 20, Pollo 2/0.2 = 10 → min = 10.
func TestRecipeAvailability_MinOverIngredients(t *testing.T) {
	db := setupRecipeCostDB(t)
	tenantID := "tenant-avail"
	recipeID := seedAlmuerzoForCost(t, db, tenantID, 3, 2)

	r := mountRecipeCostHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/recipes/"+recipeID+"/availability", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			AvailableUnits int `json:"available_units"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 10, resp.Data.AvailableUnits,
		"min(floor(3/0.15), floor(2/0.2)) = min(20,10) = 10")
}

// A recipe with zero stock on one insumo is unavailable.
func TestRecipeAvailability_ZeroWhenIngredientEmpty(t *testing.T) {
	db := setupRecipeCostDB(t)
	tenantID := "tenant-empty"
	recipeID := seedAlmuerzoForCost(t, db, tenantID, 5, 0)

	r := mountRecipeCostHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/recipes/"+recipeID+"/availability", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			AvailableUnits int `json:"available_units"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Data.AvailableUnits, "an empty insumo makes the plato unavailable")
}

// Spec §9 — a recipe with no ingredients has unlimited availability
// (advertencia, no bloquea). We return -1 to signal "ilimitada".
func TestRecipeAvailability_RecipeWithoutIngredients(t *testing.T) {
	db := setupRecipeCostDB(t)
	tenantID := "tenant-noing"
	recipeID := "b9000000-0000-4000-8000-000000000030"
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Receta vacía",
		SalePrice:   1000,
	}).Error)

	r := mountRecipeCostHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/recipes/"+recipeID+"/availability", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			AvailableUnits int `json:"available_units"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, -1, resp.Data.AvailableUnits, "no ingredients → unlimited (-1)")
}

// Art. III — cost/availability of another tenant's recipe is a 404.
func TestRecipeCost_TenantIsolation(t *testing.T) {
	db := setupRecipeCostDB(t)
	recipeID := seedAlmuerzoForCost(t, db, "tenant-owner", 10, 10)

	r := mountRecipeCostHandler(db, "tenant-intruder")
	wc := doJSON(t, r, http.MethodGet, "/recipes/"+recipeID+"/cost", nil)
	assert.Equal(t, http.StatusNotFound, wc.Code)
	wa := doJSON(t, r, http.MethodGet, "/recipes/"+recipeID+"/availability", nil)
	assert.Equal(t, http.StatusNotFound, wa.Code)
}
