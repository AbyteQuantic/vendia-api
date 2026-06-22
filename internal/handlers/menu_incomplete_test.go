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

func TestIncompleteMenuItems_ListsMenuWithoutRecipe(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.Recipe{}, &models.RecipeIngredient{}))
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Bandeja", Price: 18000, IsMenuItem: true}).Error)
	// uno completo (con receta+ingrediente) NO debe aparecer.
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p2"}, TenantID: "t1", Name: "Sopa", IsMenuItem: true}).Error)
	pid := "p2"
	require.NoError(t, db.Create(&models.Recipe{BaseModel: models.BaseModel{ID: "r2"}, TenantID: "t1", ProductID: &pid}).Error)
	require.NoError(t, db.Create(&models.RecipeIngredient{BaseModel: models.BaseModel{ID: "ri"}, RecipeUUID: "r2"}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/menu/incomplete", handlers.IncompleteMenuItems(db))
	w := doJSON(t, r, http.MethodGet, "/menu/incomplete", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "Bandeja", resp.Data[0]["name"])
}

func TestCreateRecipe_LinksExistingMenuItem_NoDuplicate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.Recipe{}, &models.RecipeIngredient{}, &models.Ingredient{}))
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "plato"}, TenantID: "t1", Name: "Bandeja", IsMenuItem: true}).Error)
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "arroz"}, TenantID: "t1", Name: "Arroz", UnitCost: 2800}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/recipes", handlers.CreateRecipe(db))
	body := map[string]any{
		"link_product_id": "plato", "product_name": "Bandeja", "sale_price": 18000,
		"ingredients": []map[string]any{{"ingredient_uuid": "arroz", "quantity": 0.2}},
	}
	w := doJSON(t, r, http.MethodPost, "/recipes", body)
	require.Equal(t, http.StatusCreated, w.Code)

	// El producto existente quedó ligado (is_recipe + recipe_id) y NO se duplicó.
	var n int64
	db.Model(&models.Product{}).Where("tenant_id = ? AND name = ?", "t1", "Bandeja").Count(&n)
	assert.Equal(t, int64(1), n, "no debe duplicar el plato")
	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", "plato").Error)
	assert.True(t, p.IsRecipe)
	require.NotNil(t, p.RecipeID)
}
