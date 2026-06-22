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

func TestRecipeMenuUsage_AndDeleteStrips(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Recipe{}, &models.Product{}, &models.WeeklyMenuPlan{}, &models.MenuPlanOverride{}))
	require.NoError(t, db.Create(&models.Recipe{BaseModel: models.BaseModel{ID: "r1"}, TenantID: "t1", ProductName: "Bandeja", SalePrice: 12000}).Error)
	// Plan semanal con la receta el lunes y el martes.
	days := `{"mon":{"enabled":true,"items":[{"recipe_uuid":"r1","planned_qty":0}]},"tue":{"enabled":true,"items":[{"recipe_uuid":"r1","planned_qty":0},{"recipe_uuid":"r2","planned_qty":0}]}}`
	require.NoError(t, db.Create(&models.WeeklyMenuPlan{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", BranchID: "", Days: days}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/recipes/:uuid/menu-usage", handlers.RecipeMenuUsage(db))
	r.DELETE("/recipes/:uuid", handlers.DeleteRecipe(db))

	// menu-usage → en menú, días lunes y martes.
	w := doJSON(t, r, http.MethodGet, "/recipes/r1/menu-usage", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			InMenu    bool     `json:"in_menu"`
			DayLabels []string `json:"day_labels"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Data.InMenu)
	assert.Equal(t, []string{"lunes", "martes"}, resp.Data.DayLabels)

	// DELETE → quita r1 de los menús (r2 sigue), borra la receta.
	wd := doJSON(t, r, http.MethodDelete, "/recipes/r1", nil)
	require.Equal(t, http.StatusOK, wd.Code)
	var plan models.WeeklyMenuPlan
	require.NoError(t, db.First(&plan, "id = ?", "p1").Error)
	assert.NotContains(t, plan.Days, "r1")
	assert.Contains(t, plan.Days, "r2") // no tocó las otras recetas
	var cnt int64
	db.Model(&models.Recipe{}).Where("id = ?", "r1").Count(&cnt)
	assert.Equal(t, int64(0), cnt)
}
