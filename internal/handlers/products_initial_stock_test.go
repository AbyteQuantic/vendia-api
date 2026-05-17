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

// setupProductInitialStockDB migrates the schema CreateProduct touches:
// Product (the row created) and InventoryMovement (the kardex trail the
// initial_stock movement lands in).
func setupProductInitialStockDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{},
		&models.InventoryMovement{},
	))
	return db
}

func mountCreateProductHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	// catalogSvc is nil — CreateProduct guards against it.
	r.POST("/products", handlers.CreateProduct(db, nil))
	return r
}

// FR-03 / AC-03 — creating a product with stock inicial 50 must record
// an `initial_stock` kardex movement with stock_before=0 and
// stock_after=50. The bug logged the movement AFTER the product row was
// written, so the self-read saw stock=50 and recorded 50 → 100.
func TestCreateProduct_InitialStockMovementSnapshot(t *testing.T) {
	db := setupProductInitialStockDB(t)
	tenantID := "tenant-pis"

	r := mountCreateProductHandler(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    "c1000000-0000-4000-8000-000000000050",
		"name":  "Gaseosa",
		"price": 2500,
		"stock": 50,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var mov models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementInitialStock).
		First(&mov).Error)

	assert.Equal(t, float64(0), mov.StockBefore, "stock_before must be 0")
	assert.Equal(t, float64(50), mov.StockAfter, "stock_after must be stock_inicial")
	assert.Equal(t, float64(50), mov.Quantity, "quantity must be the full initial stock")
	assert.Equal(t, tenantID, mov.TenantID, "movement is tenant-scoped")

	// The persisted product still carries the right stock.
	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", "c1000000-0000-4000-8000-000000000050").Error)
	assert.Equal(t, 50, product.Stock)
}

// FR-03 — a product created with zero stock logs NO initial_stock
// movement (nothing entered inventory).
func TestCreateProduct_ZeroStockLogsNoMovement(t *testing.T) {
	db := setupProductInitialStockDB(t)
	tenantID := "tenant-pis-zero"

	r := mountCreateProductHandler(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    "c1000000-0000-4000-8000-000000000051",
		"name":  "Producto sin stock",
		"price": 1000,
		"stock": 0,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var count int64
	db.Model(&models.InventoryMovement{}).Count(&count)
	assert.Equal(t, int64(0), count, "no movement for a zero-stock product")
}
