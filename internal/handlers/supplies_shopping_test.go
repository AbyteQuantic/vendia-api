// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
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

func setupShopDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Ingredient{}, &models.IngredientPrice{}))
	return db
}

func mountShop(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/supplies/shopping-list", handlers.SuppliesShoppingList(db))
	return r
}

func TestShoppingList_ShortfallAndPrice(t *testing.T) {
	db := setupShopDB(t)
	// Arroz: necesita 5 kg, tiene 2 → falta 3. Última compra $2.800/kg.
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "arroz"}, TenantID: "t1", Name: "Arroz", Unit: "kg", Stock: 2, UnitCost: 2800}).Error)
	// Papa: necesita 4, tiene 10 → NO falta (no aparece).
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "papa"}, TenantID: "t1", Name: "Papa", Unit: "kg", Stock: 10, UnitCost: 1500}).Error)

	r := mountShop(db)
	w := doJSON(t, r, http.MethodPost, "/supplies/shopping-list", map[string]any{
		"needs": []map[string]any{
			{"ingredient_id": "arroz", "name": "Arroz", "unit": "kg", "qty": 5},
			{"ingredient_id": "papa", "name": "Papa", "unit": "kg", "qty": 4},
		},
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Items          []handlers.ShoppingItem `json:"items"`
			TotalEstimated float64                 `json:"total_estimated"`
			HasEstimate    bool                    `json:"has_estimate"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Items, 1) // solo Arroz falta
	it := resp.Data.Items[0]
	assert.Equal(t, "Arroz", it.Name)
	assert.InDelta(t, 3, it.Shortfall, 0.001)
	assert.InDelta(t, 2800, it.PricePerUnit, 0.001)
	assert.InDelta(t, 8400, it.EstimatedCost, 0.001) // 3 × 2800
	assert.Equal(t, "ultima_compra", it.PriceSource)
	assert.True(t, it.IsEstimate)
	assert.InDelta(t, 8400, resp.Data.TotalEstimated, 0.001)
}

func TestShoppingList_PrefersVendiaCatalogPrice(t *testing.T) {
	db := setupShopDB(t)
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "arroz"}, TenantID: "t1", Name: "Arroz", Unit: "kg", Stock: 0, UnitCost: 2800}).Error)
	// Precio de catálogo VendIA (fuente confiable) → debe ganar sobre última compra.
	require.NoError(t, db.Create(&models.IngredientPrice{
		BaseModel: models.BaseModel{ID: "ip1"}, TenantID: "t1", IngredientID: ptr("arroz"),
		RawName: "Arroz", Source: models.PriceSourceVendiaCatalog, UnitPrice: 2500,
		PricePerBaseUnit: 2500, Confidence: 0.9, CapturedAt: time.Now(),
	}).Error)

	r := mountShop(db)
	w := doJSON(t, r, http.MethodPost, "/supplies/shopping-list", map[string]any{
		"needs": []map[string]any{{"ingredient_id": "arroz", "name": "Arroz", "unit": "kg", "qty": 4}},
	})
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Items []handlers.ShoppingItem `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Items, 1)
	assert.InDelta(t, 2500, resp.Data.Items[0].PricePerUnit, 0.001) // catálogo gana
	assert.Equal(t, "vendia_catalog", resp.Data.Items[0].PriceSource)
	assert.False(t, resp.Data.Items[0].IsEstimate) // catálogo VendIA = no estimado
}

func ptr(s string) *string { return &s }
