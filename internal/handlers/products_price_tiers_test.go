// Spec: specs/029-precios-multi-tier/spec.md
package handlers_test

import (
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

// ── T-06 (F029): products gain 3 optional tier prices ──────────────────────
//
// Tests use SQLite + AutoMigrate (same pattern as
// products_initial_stock_test.go) so they run in CI without Docker.
// SQLite enforces typeless NUMERIC; the test only asserts roundtripping
// through Product and the validation rules at the handler boundary.

func setupTiersProductDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{},
		&models.InventoryMovement{},
	))
	return db
}

func mountCreateProductRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/products", handlers.CreateProduct(db, nil))
	r.PATCH("/products/:id", handlers.UpdateProduct(db, nil))
	return r
}

// TestCreateProduct_WithPriceTiers_Persists verifies that POST product
// with the 3 tier prices roundtrips through to Product.PriceTier1/2/3
// (F029 FR-03, FR-04).
func TestCreateProduct_WithPriceTiers_Persists(t *testing.T) {
	db := setupTiersProductDB(t)
	tenantID := "tenant-tiers"

	r := mountCreateProductRouter(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":           "c1000000-0000-4000-8000-000000000291",
		"name":         "Cemento Fortecem",
		"price":        28500,
		"stock":        10,
		"price_tier_1": 25000,
		"price_tier_2": 26500,
		"price_tier_3": 28500,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var product models.Product
	require.NoError(t, db.First(&product,
		"id = ?", "c1000000-0000-4000-8000-000000000291").Error)

	require.NotNil(t, product.PriceTier1, "tier 1 debe persistir como NUMERIC, no NULL")
	require.NotNil(t, product.PriceTier2)
	require.NotNil(t, product.PriceTier3)
	assert.InDelta(t, 25000.0, *product.PriceTier1, 0.001)
	assert.InDelta(t, 26500.0, *product.PriceTier2, 0.001)
	assert.InDelta(t, 28500.0, *product.PriceTier3, 0.001)
	assert.InDelta(t, 28500.0, product.Price, 0.001,
		"el price retail no debe ser sobreescrito por los tiers")
}

// TestCreateProduct_NoTiers_LeavesNull verifies the retrocompat path:
// a POST without tier fields leaves all 3 columns NULL (F029 FR-10).
func TestCreateProduct_NoTiers_LeavesNull(t *testing.T) {
	db := setupTiersProductDB(t)
	tenantID := "tenant-no-tiers"

	r := mountCreateProductRouter(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    "c1000000-0000-4000-8000-000000000292",
		"name":  "Arepa",
		"price": 1500,
		"stock": 20,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var product models.Product
	require.NoError(t, db.First(&product,
		"id = ?", "c1000000-0000-4000-8000-000000000292").Error)
	assert.Nil(t, product.PriceTier1, "sin tiers en el payload, NULL en BD")
	assert.Nil(t, product.PriceTier2)
	assert.Nil(t, product.PriceTier3)
}

// TestCreateProduct_NegativeTierPrice_400 verifies the >0 invariant
// (Spec F029 §5 — cada precio > 0).
func TestCreateProduct_NegativeTierPrice_400(t *testing.T) {
	db := setupTiersProductDB(t)
	tenantID := "tenant-neg"

	r := mountCreateProductRouter(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":           "c1000000-0000-4000-8000-000000000293",
		"name":         "Producto inválido",
		"price":        1000,
		"price_tier_1": -5,
	})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// Nothing should have persisted.
	var count int64
	db.Model(&models.Product{}).Where("id = ?", "c1000000-0000-4000-8000-000000000293").Count(&count)
	assert.Equal(t, int64(0), count, "un tier price negativo aborta la creación entera")
}

// TestCreateProduct_ZeroTierPrice_400 verifies that 0 is also invalid
// (FR-04 — strictly positive). 0 is distinguishable from null via
// the pointer in JSON; the handler must reject it.
func TestCreateProduct_ZeroTierPrice_400(t *testing.T) {
	db := setupTiersProductDB(t)
	tenantID := "tenant-zero"

	r := mountCreateProductRouter(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":           "c1000000-0000-4000-8000-000000000294",
		"name":         "Producto",
		"price":        1000,
		"price_tier_2": 0,
	})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestUpdateProduct_SetsPriceTier verifies PATCH with a tier price
// persists on an existing product (F029 FR-03 — editable como parte
// del flujo Editar Producto cuando la capacidad está ON).
func TestUpdateProduct_SetsPriceTier(t *testing.T) {
	db := setupTiersProductDB(t)
	tenantID := "tenant-patch"

	r := mountCreateProductRouter(db, tenantID)
	// Crear sin tiers.
	wc := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    "c1000000-0000-4000-8000-000000000295",
		"name":  "Cemento",
		"price": 30000,
		"stock": 5,
	})
	require.Equal(t, http.StatusCreated, wc.Code)

	// Ahora PATCH agrega tiers.
	wu := doJSON(t, r, http.MethodPatch, "/products/c1000000-0000-4000-8000-000000000295", map[string]any{
		"price_tier_1": 27000,
		"price_tier_3": 30000,
	})
	require.Equal(t, http.StatusOK, wu.Code, wu.Body.String())

	var product models.Product
	require.NoError(t, db.First(&product,
		"id = ?", "c1000000-0000-4000-8000-000000000295").Error)
	require.NotNil(t, product.PriceTier1)
	require.NotNil(t, product.PriceTier3)
	assert.InDelta(t, 27000.0, *product.PriceTier1, 0.001)
	assert.InDelta(t, 30000.0, *product.PriceTier3, 0.001)
	assert.Nil(t, product.PriceTier2, "tier 2 no enviado → sigue NULL")
}

// TestUpdateProduct_NegativeTierPrice_400 verifies the >0 invariant
// also applies on PATCH.
func TestUpdateProduct_NegativeTierPrice_400(t *testing.T) {
	db := setupTiersProductDB(t)
	tenantID := "tenant-patch-neg"

	r := mountCreateProductRouter(db, tenantID)
	wc := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    "c1000000-0000-4000-8000-000000000296",
		"name":  "Producto",
		"price": 1000,
	})
	require.Equal(t, http.StatusCreated, wc.Code)

	wu := doJSON(t, r, http.MethodPatch, "/products/c1000000-0000-4000-8000-000000000296", map[string]any{
		"price_tier_1": -100,
	})
	require.Equal(t, http.StatusBadRequest, wu.Code, wu.Body.String())
}
