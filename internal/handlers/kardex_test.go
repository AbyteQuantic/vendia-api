// Spec: specs/001-insumos-recetas/spec.md
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

// setupKardexDB migrates the schema ProductKardex touches: Product (the
// vendible item), Ingredient (the insumo) and InventoryMovement (the
// kardex trail, which carries movements for BOTH).
func setupKardexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{},
		&models.Ingredient{},
		&models.InventoryMovement{},
		&models.Branch{},
	))
	return db
}

func mountKardexHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/inventory/kardex", handlers.ProductKardex(db))
	return r
}

// AC-07 — a recipe_consumption movement whose product_id is the UUID of
// an INSUMO (not a product) must be visible in the kardex. The kardex
// resolves the entity name from ingredients when products has no row.
func TestProductKardex_ShowsIngredientMovements(t *testing.T) {
	db := setupKardexDB(t)
	tenantID := "tenant-kardex"

	insumoID := "c1000000-0000-4000-8000-000000000070"
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: insumoID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg,
		Stock: 2.7, UnitCost: 2900,
	}).Error)

	// A recipe_consumption movement for the insumo — exactly what
	// ExplodeRecipe writes (product_id = ingredient UUID).
	require.NoError(t, db.Create(&models.InventoryMovement{
		ID:           "9c000000-0000-4000-8000-000000000070",
		TenantID:     tenantID,
		ProductID:    insumoID,
		ProductName:  "Arroz",
		MovementType: models.MovementRecipeConsumption,
		Quantity:     -0.3,
		StockBefore:  3,
		StockAfter:   2.7,
		CreatedAt:    time.Now(),
	}).Error)

	r := mountKardexHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+insumoID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Product struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"product"`
			Movements []models.InventoryMovement `json:"movements"`
			Total     int64                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, insumoID, resp.Data.Product.ID)
	assert.Equal(t, "Arroz", resp.Data.Product.Name, "insumo name resolved from ingredients")
	require.Len(t, resp.Data.Movements, 1, "the recipe_consumption movement must be visible")
	assert.Equal(t, models.MovementRecipeConsumption, resp.Data.Movements[0].MovementType)
	assert.Equal(t, int64(1), resp.Data.Total)
}

// Regression — the kardex of a normal vendible product still works:
// resolving from products takes priority and movements are returned.
func TestProductKardex_ProductMovementsStillWork(t *testing.T) {
	db := setupKardexDB(t)
	tenantID := "tenant-kardex"

	productID := "a1000000-0000-4000-8000-000000000071"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500, Stock: 47,
	}).Error)
	require.NoError(t, db.Create(&models.InventoryMovement{
		ID:           "9d000000-0000-4000-8000-000000000071",
		TenantID:     tenantID,
		ProductID:    productID,
		ProductName:  "Gaseosa",
		MovementType: models.MovementSale,
		Quantity:     -3,
		StockBefore:  50,
		StockAfter:   47,
		CreatedAt:    time.Now(),
	}).Error)

	r := mountKardexHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+productID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Product struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Stock int    `json:"stock"`
			} `json:"product"`
			Movements []models.InventoryMovement `json:"movements"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, productID, resp.Data.Product.ID)
	assert.Equal(t, "Gaseosa", resp.Data.Product.Name)
	assert.Equal(t, 47, resp.Data.Product.Stock)
	require.Len(t, resp.Data.Movements, 1)
	assert.Equal(t, models.MovementSale, resp.Data.Movements[0].MovementType)
}

// An id that exists in neither products nor ingredients for the tenant
// is a 404 — the kardex never invents an entity.
func TestProductKardex_UnknownIDReturns404(t *testing.T) {
	db := setupKardexDB(t)
	r := mountKardexHandler(db, "tenant-kardex")
	w := doJSON(t, r, http.MethodGet,
		"/inventory/kardex?product_id=99999999-0000-4000-8000-000000000099", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// Art. III — an insumo owned by another tenant is invisible in the
// kardex: the request resolves to a 404, never a cross-tenant leak.
func TestProductKardex_IngredientTenantIsolation(t *testing.T) {
	db := setupKardexDB(t)
	foreignInsumo := "c2000000-0000-4000-8000-000000000072"
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: foreignInsumo},
		TenantID:  "tenant-other", Name: "Arroz ajeno", Unit: models.UnitKg,
	}).Error)

	r := mountKardexHandler(db, "tenant-kardex")
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+foreignInsumo, nil)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}
