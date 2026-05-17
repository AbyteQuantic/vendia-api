// Spec: specs/005-fixes-regresion-360/spec.md
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

// setupIngredientKardexDB migrates Ingredient (the row created) and
// InventoryMovement (where the initial_stock movement lands).
func setupIngredientKardexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Ingredient{},
		&models.InventoryMovement{},
	))
	return db
}

func mountCreateIngredientHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/ingredients", handlers.CreateIngredient(db))
	return r
}

// FR-05 / AC-05 — creating an insumo with stock inicial 10 must record
// an `initial_stock` kardex movement (stock_before=0, stock_after=10)
// so the invariant `stock = Σ movimientos` holds for insumos exactly
// like it already does for products.
func TestCreateIngredient_LogsInitialStockMovement(t *testing.T) {
	db := setupIngredientKardexDB(t)
	tenantID := "tenant-iks"

	ingredientID := "c1000000-0000-4000-8000-000000000060"
	r := mountCreateIngredientHandler(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/ingredients", map[string]any{
		"id":        ingredientID,
		"name":      "Arroz",
		"unit":      "kg",
		"stock":     10,
		"unit_cost": 2900,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var mov models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementInitialStock).
		First(&mov).Error)

	assert.Equal(t, ingredientID, mov.ProductID, "movement points at the insumo UUID")
	assert.Equal(t, tenantID, mov.TenantID, "movement is tenant-scoped")
	assert.Equal(t, float64(0), mov.StockBefore, "stock_before must be 0")
	assert.Equal(t, float64(10), mov.StockAfter, "stock_after must be stock_inicial")
	assert.Equal(t, float64(10), mov.Quantity, "quantity must be the full initial stock")

	// Invariant: ingredient stock equals the sum of its movements.
	var sumMovements float64
	db.Model(&models.InventoryMovement{}).
		Where("product_id = ?", ingredientID).
		Select("COALESCE(SUM(quantity), 0)").
		Scan(&sumMovements)

	var ingredient models.Ingredient
	require.NoError(t, db.First(&ingredient, "id = ?", ingredientID).Error)
	assert.Equal(t, ingredient.Stock, sumMovements,
		"stock = Σ movimientos must hold for insumos (AC-05)")
}

// FR-05 — an insumo created with zero stock logs NO movement.
func TestCreateIngredient_ZeroStockLogsNoMovement(t *testing.T) {
	db := setupIngredientKardexDB(t)
	tenantID := "tenant-iks-zero"

	r := mountCreateIngredientHandler(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/ingredients", map[string]any{
		"id":        "c1000000-0000-4000-8000-000000000061",
		"name":      "Insumo sin stock",
		"unit":      "kg",
		"stock":     0,
		"unit_cost": 1000,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var count int64
	db.Model(&models.InventoryMovement{}).Count(&count)
	assert.Equal(t, int64(0), count, "no movement for a zero-stock insumo")
}

// FR-05 — re-sending an existing insumo UUID (idempotent re-sync) must
// NOT log a second initial_stock movement.
func TestCreateIngredient_IdempotentResyncLogsNoExtraMovement(t *testing.T) {
	db := setupIngredientKardexDB(t)
	tenantID := "tenant-iks-idem"

	ingredientID := "c1000000-0000-4000-8000-000000000062"
	payload := map[string]any{
		"id":        ingredientID,
		"name":      "Arroz",
		"unit":      "kg",
		"stock":     10,
		"unit_cost": 2900,
	}
	r := mountCreateIngredientHandler(db, tenantID)

	w1 := doJSON(t, r, http.MethodPost, "/ingredients", payload)
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())
	w2 := doJSON(t, r, http.MethodPost, "/ingredients", payload)
	require.Equal(t, http.StatusCreated, w2.Code, w2.Body.String())

	var count int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementInitialStock).
		Count(&count)
	assert.Equal(t, int64(1), count, "re-sync must not double-log the initial_stock movement")
}
