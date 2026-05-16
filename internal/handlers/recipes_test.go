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

// setupRecipeHandlerDB migrates the schema CreateRecipe touches:
// Recipe, RecipeIngredient, Ingredient (the insumo it snapshots) and
// Product — FR-02, CreateRecipe now also creates the vendible
// product-receta in the same transaction.
func setupRecipeHandlerDB(t *testing.T) *gorm.DB {
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

func mountCreateRecipeHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/recipes", handlers.CreateRecipe(db))
	return r
}

// seedInsumo persists one insumo for a tenant and returns its UUID.
func seedInsumo(t *testing.T, db *gorm.DB, tenantID, id, name string, unitCost float64) string {
	t.Helper()
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: id},
		TenantID:  tenantID, Name: name, Unit: models.UnitKg,
		Stock: 100, UnitCost: unitCost,
	}).Error)
	return id
}

// AC-01 (insumo contract) — POST /recipes with the new ingredient_uuid
// body resolves each insumo and snapshots its name + unit_cost onto the
// recipe line. The client never sends product_name or unit_cost.
func TestCreateRecipe_SnapshotsInsumoNameAndCost(t *testing.T) {
	db := setupRecipeHandlerDB(t)
	tenantID := "tenant-recipe"
	arrozID := seedInsumo(t, db, tenantID, "c1000000-0000-4000-8000-000000000050", "Arroz", 2900)
	polloID := seedInsumo(t, db, tenantID, "c2000000-0000-4000-8000-000000000050", "Pollo", 12000)

	r := mountCreateRecipeHandler(db, tenantID)
	payload := map[string]any{
		"product_name": "Almuerzo corriente",
		"category":     "Almuerzos",
		"sale_price":   12000,
		"emoji":        "🍛",
		"photo_url":    "",
		"ingredients": []map[string]any{
			{"ingredient_uuid": arrozID, "quantity": 0.15},
			{"ingredient_uuid": polloID, "quantity": 0.2},
		},
	}
	w := doJSON(t, r, http.MethodPost, "/recipes", payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Recipe `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Ingredients, 2)

	// Lines are snapshotted from the insumos, not echoed from the body.
	byName := map[string]models.RecipeIngredient{}
	for _, ing := range resp.Data.Ingredients {
		byName[ing.ProductName] = ing
	}
	arroz, ok := byName["Arroz"]
	require.True(t, ok, "arroz line missing")
	assert.InDelta(t, 0.15, arroz.Quantity, 1e-9)
	assert.InDelta(t, 2900, arroz.UnitCost, 1e-9, "unit cost snapshotted from insumo")
	require.NotNil(t, arroz.IngredientID)
	assert.Equal(t, arrozID, *arroz.IngredientID, "IngredientID resolved from request")

	pollo, ok := byName["Pollo"]
	require.True(t, ok, "pollo line missing")
	assert.InDelta(t, 0.2, pollo.Quantity, 1e-9)
	assert.InDelta(t, 12000, pollo.UnitCost, 1e-9)
	require.NotNil(t, pollo.IngredientID)
	assert.Equal(t, polloID, *pollo.IngredientID)

	// The recipe persists with the snapshotted lines.
	var stored models.RecipeIngredient
	require.NoError(t, db.Where("recipe_uuid = ? AND product_name = ?", resp.Data.ID, "Arroz").
		First(&stored).Error)
	assert.InDelta(t, 2900, stored.UnitCost, 1e-9)
	require.NotNil(t, stored.IngredientID)
	assert.Equal(t, arrozID, *stored.IngredientID)
}

// An ingredient_uuid that does not exist for the tenant rejects the
// whole request with 400 and a Spanish message (Art. III + Cero
// Fricción: never persist a recipe pointing at a phantom insumo).
func TestCreateRecipe_RejectsUnknownInsumo(t *testing.T) {
	db := setupRecipeHandlerDB(t)
	tenantID := "tenant-recipe"
	arrozID := seedInsumo(t, db, tenantID, "c1000000-0000-4000-8000-000000000051", "Arroz", 2900)

	r := mountCreateRecipeHandler(db, tenantID)
	payload := map[string]any{
		"product_name": "Almuerzo corriente",
		"sale_price":   12000,
		"ingredients": []map[string]any{
			{"ingredient_uuid": arrozID, "quantity": 0.15},
			{"ingredient_uuid": "99999999-0000-4000-8000-000000000099", "quantity": 0.2},
		},
	}
	w := doJSON(t, r, http.MethodPost, "/recipes", payload)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var resp struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "insumo no existe")

	// Nothing is persisted when validation fails.
	var count int64
	require.NoError(t, db.Model(&models.Recipe{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "a rejected recipe must not persist")
}

// Art. III — an insumo owned by another tenant is invisible: the
// request is rejected exactly like an unknown insumo.
func TestCreateRecipe_RejectsCrossTenantInsumo(t *testing.T) {
	db := setupRecipeHandlerDB(t)
	foreignID := seedInsumo(t, db, "tenant-other", "c3000000-0000-4000-8000-000000000052", "Arroz ajeno", 2900)

	r := mountCreateRecipeHandler(db, "tenant-recipe")
	payload := map[string]any{
		"product_name": "Almuerzo",
		"sale_price":   12000,
		"ingredients": []map[string]any{
			{"ingredient_uuid": foreignID, "quantity": 0.15},
		},
	}
	w := doJSON(t, r, http.MethodPost, "/recipes", payload)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// Binding rules: product_name, sale_price > 0, at least one ingredient,
// and each ingredient needs ingredient_uuid + quantity > 0.
func TestCreateRecipe_Validation(t *testing.T) {
	db := setupRecipeHandlerDB(t)
	tenantID := "tenant-recipe"
	insumoID := seedInsumo(t, db, tenantID, "c1000000-0000-4000-8000-000000000053", "Pan", 500)
	r := mountCreateRecipeHandler(db, tenantID)

	cases := []struct {
		name    string
		payload map[string]any
		code    int
	}{
		{
			name: "missing product_name",
			payload: map[string]any{
				"sale_price":  5000,
				"ingredients": []map[string]any{{"ingredient_uuid": insumoID, "quantity": 1}},
			},
			code: http.StatusBadRequest,
		},
		{
			name: "zero sale_price",
			payload: map[string]any{
				"product_name": "Perro",
				"sale_price":   0,
				"ingredients":  []map[string]any{{"ingredient_uuid": insumoID, "quantity": 1}},
			},
			code: http.StatusBadRequest,
		},
		{
			name: "empty ingredients",
			payload: map[string]any{
				"product_name": "Perro",
				"sale_price":   5000,
				"ingredients":  []map[string]any{},
			},
			code: http.StatusBadRequest,
		},
		{
			name: "ingredient missing ingredient_uuid",
			payload: map[string]any{
				"product_name": "Perro",
				"sale_price":   5000,
				"ingredients":  []map[string]any{{"quantity": 1}},
			},
			code: http.StatusBadRequest,
		},
		{
			name: "ingredient zero quantity",
			payload: map[string]any{
				"product_name": "Perro",
				"sale_price":   5000,
				"ingredients":  []map[string]any{{"ingredient_uuid": insumoID, "quantity": 0}},
			},
			code: http.StatusBadRequest,
		},
		{
			name: "valid recipe",
			payload: map[string]any{
				"product_name": "Perro Caliente",
				"sale_price":   5000,
				"ingredients":  []map[string]any{{"ingredient_uuid": insumoID, "quantity": 1}},
			},
			code: http.StatusCreated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, r, http.MethodPost, "/recipes", tc.payload)
			assert.Equal(t, tc.code, w.Code, "case: %s, body: %s", tc.name, w.Body.String())
		})
	}
}
