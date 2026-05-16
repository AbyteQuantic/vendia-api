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

// setupIngredientDB builds an in-memory sqlite DB with the Ingredient
// schema migrated. The Ingredient struct carries no Postgres-only
// defaults, so AutoMigrate works directly on sqlite (unlike the
// hand-crafted schema branch_isolation_test needs for Product/Sale).
func setupIngredientDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Ingredient{}))
	return db
}

func mountIngredientsHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/ingredients", handlers.ListIngredients(db))
	r.POST("/ingredients", handlers.CreateIngredient(db))
	r.PATCH("/ingredients/:uuid", handlers.UpdateIngredient(db))
	r.DELETE("/ingredients/:uuid", handlers.DeleteIngredient(db))
	r.GET("/ingredients/low-stock", handlers.LowStockIngredients(db))
	return r
}

// doJSON is the shared JSON-request helper declared in branches_test.go.

// AC-01 — create an ingredient, then read it back: stock + unit visible.
func TestCreateIngredient_PersistsAndEchoes(t *testing.T) {
	db := setupIngredientDB(t)
	r := mountIngredientsHandler(db, "tenant-a")

	w := doJSON(t, r, http.MethodPost, "/ingredients", map[string]any{
		"name":      "Arroz",
		"unit":      "kg",
		"stock":     10,
		"min_stock": 2,
		"unit_cost": 2900,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Ingredient `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "Arroz", resp.Data.Name)
	assert.Equal(t, "kg", resp.Data.Unit)
	assert.Equal(t, float64(10), resp.Data.Stock)
	assert.Equal(t, "tenant-a", resp.Data.TenantID)
	assert.NotEmpty(t, resp.Data.ID)
}

// Unit defaults to "unidad" when omitted (Plan §3 default).
func TestCreateIngredient_DefaultsUnitToUnidad(t *testing.T) {
	db := setupIngredientDB(t)
	r := mountIngredientsHandler(db, "tenant-a")

	w := doJSON(t, r, http.MethodPost, "/ingredients", map[string]any{
		"name": "Huevos",
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp struct {
		Data models.Ingredient `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.UnitUnidad, resp.Data.Unit)
}

// VI — invalid unit enum is rejected with a Spanish 400.
func TestCreateIngredient_RejectsInvalidUnit(t *testing.T) {
	db := setupIngredientDB(t)
	r := mountIngredientsHandler(db, "tenant-a")

	w := doJSON(t, r, http.MethodPost, "/ingredients", map[string]any{
		"name": "Arroz",
		"unit": "kilo",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "unidad")
}

// VI — name is required.
func TestCreateIngredient_RejectsMissingName(t *testing.T) {
	db := setupIngredientDB(t)
	r := mountIngredientsHandler(db, "tenant-a")

	w := doJSON(t, r, http.MethodPost, "/ingredients", map[string]any{
		"unit": "kg",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// VI — negative stock / cost are rejected (inventory must be exact).
func TestCreateIngredient_RejectsNegativeNumbers(t *testing.T) {
	db := setupIngredientDB(t)
	r := mountIngredientsHandler(db, "tenant-a")

	for _, field := range []string{"stock", "min_stock", "unit_cost"} {
		payload := map[string]any{"name": "X", "unit": "kg"}
		payload[field] = -1
		w := doJSON(t, r, http.MethodPost, "/ingredients", payload)
		assert.Equal(t, http.StatusBadRequest, w.Code, "field %s must reject negative", field)
	}
}

// Art. III — list is scoped to the tenant; another tenant's rows never leak.
func TestListIngredients_TenantIsolation(t *testing.T) {
	db := setupIngredientDB(t)
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 5,
	}).Error)
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-b", Name: "Secreto", Unit: "kg", Stock: 99,
	}).Error)

	r := mountIngredientsHandler(db, "tenant-a")
	w := doJSON(t, r, http.MethodGet, "/ingredients", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp handlers.PaginatedResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, int64(1), resp.Total)
	raw, _ := json.Marshal(resp.Data)
	var rows []models.Ingredient
	require.NoError(t, json.Unmarshal(raw, &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "Arroz", rows[0].Name)
}

// PATCH applies partial updates only to provided fields.
func TestUpdateIngredient_PartialFields(t *testing.T) {
	db := setupIngredientDB(t)
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 5, UnitCost: 2000}
	require.NoError(t, db.Create(&ing).Error)

	r := mountIngredientsHandler(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/ingredients/"+ing.ID, map[string]any{
		"unit_cost": 3000,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var updated models.Ingredient
	require.NoError(t, db.First(&updated, "id = ?", ing.ID).Error)
	assert.Equal(t, float64(3000), updated.UnitCost)
	assert.Equal(t, "Arroz", updated.Name, "untouched field must keep its value")
}

// PATCH cannot touch another tenant's ingredient.
func TestUpdateIngredient_RejectsForeignTenant(t *testing.T) {
	db := setupIngredientDB(t)
	ing := models.Ingredient{TenantID: "tenant-b", Name: "Arroz", Unit: "kg"}
	require.NoError(t, db.Create(&ing).Error)

	r := mountIngredientsHandler(db, "tenant-a")
	w := doJSON(t, r, http.MethodPatch, "/ingredients/"+ing.ID, map[string]any{"unit_cost": 1})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// DELETE soft-deletes the ingredient.
func TestDeleteIngredient_SoftDeletes(t *testing.T) {
	db := setupIngredientDB(t)
	ing := models.Ingredient{TenantID: "tenant-a", Name: "Arroz", Unit: "kg"}
	require.NoError(t, db.Create(&ing).Error)

	r := mountIngredientsHandler(db, "tenant-a")
	w := doJSON(t, r, http.MethodDelete, "/ingredients/"+ing.ID, nil)
	require.Equal(t, http.StatusOK, w.Code)

	var count int64
	db.Model(&models.Ingredient{}).Where("id = ?", ing.ID).Count(&count)
	assert.Equal(t, int64(0), count, "soft-deleted row must not surface")
}

// AC-05 — low-stock endpoint returns only ingredients below their minimum.
func TestLowStockIngredients_ReturnsBelowMinimumOnly(t *testing.T) {
	db := setupIngredientDB(t)
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Arroz", Unit: "kg", Stock: 1, MinStock: 2,
	}).Error) // below — should appear
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Pollo", Unit: "kg", Stock: 5, MinStock: 2,
	}).Error) // above — should not appear
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-a", Name: "Sal", Unit: "g", Stock: 0, MinStock: 0,
	}).Error) // no threshold — should not appear
	require.NoError(t, db.Create(&models.Ingredient{
		TenantID: "tenant-b", Name: "Otro", Unit: "kg", Stock: 0, MinStock: 9,
	}).Error) // other tenant — must never leak

	r := mountIngredientsHandler(db, "tenant-a")
	w := doJSON(t, r, http.MethodGet, "/ingredients/low-stock", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data []models.Ingredient `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "Arroz", resp.Data[0].Name)
}

// Idempotent create — a client-supplied UUID re-sent does not duplicate.
func TestCreateIngredient_IdempotentByUUID(t *testing.T) {
	db := setupIngredientDB(t)
	r := mountIngredientsHandler(db, "tenant-a")
	id := "33333333-3333-4333-8333-333333333333"

	for i := 0; i < 2; i++ {
		w := doJSON(t, r, http.MethodPost, "/ingredients", map[string]any{
			"id": id, "name": "Arroz", "unit": "kg", "stock": 10,
		})
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.Ingredient{}).Where("id = ?", id).Count(&count)
	assert.Equal(t, int64(1), count, "re-sending the same UUID must not duplicate")
}
