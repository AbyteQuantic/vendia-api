// Spec: specs/050-min-stock-ui/spec.md
package handlers_test

import (
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupMinStockDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.InventoryMovement{}))
	return db
}

func mountMinStockRouter(db *gorm.DB, tenantID string) *gin.Engine {
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

// AC-01: crear producto con min_stock lo persiste.
func TestCreateProduct_PersistsMinStock(t *testing.T) {
	db := setupMinStockDB(t)
	r := mountMinStockRouter(db, "tenant-ms")
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":        "a1000000-0000-4000-8000-000000000501",
		"name":      "Aceite Girasol",
		"price":     9000,
		"stock":     12,
		"min_stock": 5,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", "a1000000-0000-4000-8000-000000000501").Error)
	require.Equal(t, 5, p.MinStock)
}

// AC-02: PATCH min_stock lo actualiza; omitirlo no lo toca.
func TestUpdateProduct_PatchesMinStock(t *testing.T) {
	db := setupMinStockDB(t)
	r := mountMinStockRouter(db, "tenant-ms")
	// seed
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "a1000000-0000-4000-8000-000000000502"},
		TenantID:  "tenant-ms", Name: "Arroz", Price: 4000, Stock: 20, MinStock: 3,
	}).Error)

	// PATCH solo min_stock
	w := doJSON(t, r, http.MethodPatch, "/products/a1000000-0000-4000-8000-000000000502",
		map[string]any{"min_stock": 8})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", "a1000000-0000-4000-8000-000000000502").Error)
	require.Equal(t, 8, p.MinStock)
	require.Equal(t, "Arroz", p.Name) // intacto

	// PATCH que NO manda min_stock no lo cambia.
	w2 := doJSON(t, r, http.MethodPatch, "/products/a1000000-0000-4000-8000-000000000502",
		map[string]any{"price": 4500})
	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())
	require.NoError(t, db.First(&p, "id = ?", "a1000000-0000-4000-8000-000000000502").Error)
	require.Equal(t, 8, p.MinStock) // sigue en 8
}

// AC-03: min_stock negativo → 400.
func TestCreateProduct_RejectsNegativeMinStock(t *testing.T) {
	db := setupMinStockDB(t)
	r := mountMinStockRouter(db, "tenant-ms")
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":        "a1000000-0000-4000-8000-000000000503",
		"name":      "Sal",
		"price":     2000,
		"min_stock": -1,
	})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
