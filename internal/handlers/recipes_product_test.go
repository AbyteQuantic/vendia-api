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

// setupRecipeProductDB migrates the schema the recipe CRUD touches once
// it also wires a vendible Product (FR-02): Recipe, RecipeIngredient,
// Ingredient AND Product.
func setupRecipeProductDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Ingredient{},
		&models.Product{},
		&models.WeeklyMenuPlan{},   // DeleteRecipe limpia menús (Spec 078)
		&models.MenuPlanOverride{},
	))
	return db
}

// mountRecipeCRUDHandlers mounts the full recipe CRUD so the
// product-receta wiring can be exercised end to end.
func mountRecipeCRUDHandlers(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/recipes", handlers.CreateRecipe(db))
	r.PATCH("/recipes/:uuid", handlers.UpdateRecipe(db))
	r.DELETE("/recipes/:uuid", handlers.DeleteRecipe(db))
	return r
}

// seedInsumoP persists one insumo for a tenant and returns its UUID.
func seedInsumoP(t *testing.T, db *gorm.DB, tenantID, id, name string, unitCost float64) string {
	t.Helper()
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: id},
		TenantID:  tenantID, Name: name, Unit: models.UnitKg,
		Stock: 100, UnitCost: unitCost,
	}).Error)
	return id
}

// BUG-3 / FR-02 — POST /recipes must create a vendible Product and link
// it both ways: Recipe.ProductID → Product.ID and Product.RecipeID →
// Recipe.ID. Without this no product-receta can be sold in the POS.
func TestCreateRecipe_CreatesLinkedVendibleProduct(t *testing.T) {
	db := setupRecipeProductDB(t)
	tenantID := "tenant-rp"
	arrozID := seedInsumoP(t, db, tenantID, "c1000000-0000-4000-8000-000000000080", "Arroz", 2900)

	r := mountRecipeCRUDHandlers(db, tenantID)
	payload := map[string]any{
		"product_name": "Almuerzo corriente",
		"category":     "Almuerzos",
		"sale_price":   12000,
		"emoji":        "🍛",
		"ingredients": []map[string]any{
			{"ingredient_uuid": arrozID, "quantity": 0.15},
		},
	}
	w := doJSON(t, r, http.MethodPost, "/recipes", payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Recipe `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// The response reflects the recipe WITH its product_id (FR-02).
	require.NotNil(t, resp.Data.ProductID, "POST /recipes response must carry product_id")
	require.NotEmpty(t, *resp.Data.ProductID)

	// A vendible Product exists and is flagged as a recipe.
	var product models.Product
	require.NoError(t, db.Where("id = ?", *resp.Data.ProductID).First(&product).Error)
	assert.True(t, product.IsRecipe, "the created product must be a recipe product")
	assert.Equal(t, "Almuerzo corriente", product.Name)
	assert.InDelta(t, 12000, product.Price, 1e-9)
	assert.Equal(t, "Almuerzos", product.Category)
	assert.Equal(t, "🍛", product.Emoji)
	assert.Equal(t, 0, product.Stock, "a recipe product has no own stock (D1)")
	assert.Equal(t, tenantID, product.TenantID)

	// Both sides of the association point at each other.
	require.NotNil(t, product.RecipeID)
	assert.Equal(t, resp.Data.ID, *product.RecipeID, "Product.RecipeID → Recipe.ID")
	assert.Equal(t, product.ID, *resp.Data.ProductID, "Recipe.ProductID → Product.ID")

	// The recipe row persisted with its product_id set.
	var stored models.Recipe
	require.NoError(t, db.Where("id = ?", resp.Data.ID).First(&stored).Error)
	require.NotNil(t, stored.ProductID)
	assert.Equal(t, product.ID, *stored.ProductID)
}

// F043 slice manual — POST /recipes carries the optional plato
// description + portion straight onto the vendible Product so the public
// menu card shows them. is_menu_item must also stay true.
func TestCreateRecipe_CopiesDescriptionAndPortionToProduct(t *testing.T) {
	db := setupRecipeProductDB(t)
	tenantID := "tenant-rp"
	arrozID := seedInsumoP(t, db, tenantID, "c1000000-0000-4000-8000-000000000090", "Arroz", 2900)

	r := mountRecipeCRUDHandlers(db, tenantID)
	payload := map[string]any{
		"product_name": "Bandeja Paisa",
		"category":     "Platos fuertes",
		"sale_price":   25000,
		"emoji":        "🍛",
		"description":  "Frijoles, arroz, carne, chicharrón y huevo",
		"portion":      "Para compartir",
		"ingredients": []map[string]any{
			{"ingredient_uuid": arrozID, "quantity": 0.2},
		},
	}
	w := doJSON(t, r, http.MethodPost, "/recipes", payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Recipe `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.ProductID)

	var product models.Product
	require.NoError(t, db.Where("id = ?", *resp.Data.ProductID).First(&product).Error)
	assert.Equal(t, "Frijoles, arroz, carne, chicharrón y huevo", product.Description)
	assert.Equal(t, "Para compartir", product.Portion)
	assert.True(t, product.IsMenuItem, "a recipe is also a menu item")
}

// BUG-3 — a rejected recipe (unknown insumo) must NOT leave an orphan
// product behind: the whole CreateRecipe transaction rolls back.
func TestCreateRecipe_RejectedRecipeLeavesNoOrphanProduct(t *testing.T) {
	db := setupRecipeProductDB(t)
	tenantID := "tenant-rp"

	r := mountRecipeCRUDHandlers(db, tenantID)
	payload := map[string]any{
		"product_name": "Almuerzo corriente",
		"sale_price":   12000,
		"ingredients": []map[string]any{
			{"ingredient_uuid": "99999999-0000-4000-8000-000000000099", "quantity": 0.2},
		},
	}
	w := doJSON(t, r, http.MethodPost, "/recipes", payload)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var recipeCount, productCount int64
	require.NoError(t, db.Model(&models.Recipe{}).Count(&recipeCount).Error)
	require.NoError(t, db.Model(&models.Product{}).Count(&productCount).Error)
	assert.Equal(t, int64(0), recipeCount, "no recipe persists on rejection")
	assert.Equal(t, int64(0), productCount, "no vendible product persists on rejection")
}

// BUG-3 — DELETE /recipes/:uuid must soft-delete the linked vendible
// Product so no orphan product-receta stays sellable in the POS.
func TestDeleteRecipe_SoftDeletesLinkedProduct(t *testing.T) {
	db := setupRecipeProductDB(t)
	tenantID := "tenant-rp"
	arrozID := seedInsumoP(t, db, tenantID, "c1000000-0000-4000-8000-000000000081", "Arroz", 2900)

	r := mountRecipeCRUDHandlers(db, tenantID)
	createW := doJSON(t, r, http.MethodPost, "/recipes", map[string]any{
		"product_name": "Almuerzo corriente",
		"sale_price":   12000,
		"ingredients":  []map[string]any{{"ingredient_uuid": arrozID, "quantity": 0.15}},
	})
	require.Equal(t, http.StatusCreated, createW.Code, createW.Body.String())
	var created struct {
		Data models.Recipe `json:"data"`
	}
	require.NoError(t, json.Unmarshal(createW.Body.Bytes(), &created))
	productID := *created.Data.ProductID

	delW := doJSON(t, r, http.MethodDelete, "/recipes/"+created.Data.ID, nil)
	require.Equal(t, http.StatusOK, delW.Code, delW.Body.String())

	// The recipe is gone (soft delete).
	var recipeCount int64
	require.NoError(t, db.Model(&models.Recipe{}).Where("id = ?", created.Data.ID).
		Count(&recipeCount).Error)
	assert.Equal(t, int64(0), recipeCount)

	// The linked product is gone too — no orphan vendible product.
	var productCount int64
	require.NoError(t, db.Model(&models.Product{}).Where("id = ?", productID).
		Count(&productCount).Error)
	assert.Equal(t, int64(0), productCount, "the linked product-receta must be soft-deleted")

	// But it IS still there with Unscoped — confirming a SOFT delete.
	var unscoped int64
	require.NoError(t, db.Unscoped().Model(&models.Product{}).Where("id = ?", productID).
		Count(&unscoped).Error)
	assert.Equal(t, int64(1), unscoped, "soft delete keeps the row for audit/sync")
}

// BUG-3 — PATCH /recipes/:uuid must keep the linked product's Name and
// Price in sync so the POS shows the up-to-date plato.
func TestUpdateRecipe_SyncsLinkedProductNameAndPrice(t *testing.T) {
	db := setupRecipeProductDB(t)
	tenantID := "tenant-rp"
	arrozID := seedInsumoP(t, db, tenantID, "c1000000-0000-4000-8000-000000000082", "Arroz", 2900)

	r := mountRecipeCRUDHandlers(db, tenantID)
	createW := doJSON(t, r, http.MethodPost, "/recipes", map[string]any{
		"product_name": "Almuerzo corriente",
		"sale_price":   12000,
		"ingredients":  []map[string]any{{"ingredient_uuid": arrozID, "quantity": 0.15}},
	})
	require.Equal(t, http.StatusCreated, createW.Code, createW.Body.String())
	var created struct {
		Data models.Recipe `json:"data"`
	}
	require.NoError(t, json.Unmarshal(createW.Body.Bytes(), &created))
	productID := *created.Data.ProductID

	newName := "Almuerzo especial"
	newPrice := 15000.0
	patchW := doJSON(t, r, http.MethodPatch, "/recipes/"+created.Data.ID, map[string]any{
		"product_name": newName,
		"sale_price":   newPrice,
	})
	require.Equal(t, http.StatusOK, patchW.Code, patchW.Body.String())

	var product models.Product
	require.NoError(t, db.Where("id = ?", productID).First(&product).Error)
	assert.Equal(t, newName, product.Name, "product name must follow the recipe name")
	assert.InDelta(t, newPrice, product.Price, 1e-9, "product price must follow the recipe sale_price")
}
