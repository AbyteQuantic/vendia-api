// Spec: specs/076-alistar-insumos-del-dia/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupPrepDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Ingredient{}, &models.Recipe{}, &models.RecipeIngredient{},
		&models.WeeklyMenuPlan{}, &models.MenuPlanOverride{}))
	return db
}

func TestSuppliesPrepList(t *testing.T) {
	db := setupPrepDB(t)
	tenant := "t1"
	// Insumos.
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "arroz"}, TenantID: tenant, Name: "Arroz", Unit: "kg"}).Error)
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "papa"}, TenantID: tenant, Name: "Papa", Unit: "kg"}).Error)
	// Receta "Bandeja" con yield 10 porciones: 2kg arroz + 5kg papa por las 10.
	arroz, papa := "arroz", "papa"
	require.NoError(t, db.Create(&models.Recipe{BaseModel: models.BaseModel{ID: "r1"}, TenantID: tenant, ProductName: "Bandeja", Yield: "10 porciones",
		Ingredients: []models.RecipeIngredient{
			{BaseModel: models.BaseModel{ID: "ri1"}, RecipeUUID: "r1", ProductName: "Arroz", Quantity: 2, IngredientID: &arroz},
			{BaseModel: models.BaseModel{ID: "ri2"}, RecipeUUID: "r1", ProductName: "Papa", Quantity: 5, IngredientID: &papa},
		}}).Error)
	// Menú semanal: el plato va el día de HOY, con PlannedQty 20.
	now := time.Now()
	dayKey := [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}[int(now.Weekday())]
	daysJSON := `{"` + dayKey + `":{"enabled":true,"items":[{"recipe_uuid":"r1","planned_qty":20}]}}`
	require.NoError(t, db.Create(&models.WeeklyMenuPlan{TenantID: tenant, BranchID: "", Days: daysJSON}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenant); c.Next() })
	r.GET("/supplies/prep-list", handlers.SuppliesPrepList(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/supplies/prep-list?date="+now.Format("2006-01-02"), nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Dishes []handlers.PrepDish `json:"dishes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Dishes, 1)
	d := resp.Data.Dishes[0]
	assert.Equal(t, "Bandeja", d.Name)
	assert.Equal(t, 20, d.DefaultPortions) // PlannedQty
	require.Len(t, d.Ingredients, 2)
	// AC-02: cantidad-por-porción divide por el yield (2kg/10 = 0.2; 5/10 = 0.5).
	byName := map[string]handlers.PrepIngredient{}
	for _, ing := range d.Ingredients {
		byName[ing.Name] = ing
	}
	assert.InDelta(t, 0.2, byName["Arroz"].QtyPerPortion, 0.0001)
	assert.Equal(t, "kg", byName["Arroz"].Unit)
	assert.InDelta(t, 0.5, byName["Papa"].QtyPerPortion, 0.0001)
}

func TestSuppliesPrepListEmptyWhenNoMenu(t *testing.T) {
	db := setupPrepDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/supplies/prep-list", handlers.SuppliesPrepList(db))
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/supplies/prep-list?date=2026-06-21", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"dishes":[]`)
}
