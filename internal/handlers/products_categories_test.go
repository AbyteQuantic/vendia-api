// Spec: specs/068-categorias-caracteristicas-producto/spec.md
package handlers_test

import (
	"encoding/json"
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

func setupCatDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	return db
}

func mountCatRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() })
	r.POST("/products", handlers.CreateProduct(db, nil))
	r.PATCH("/products/:id", handlers.UpdateProduct(db, nil))
	r.GET("/products/categories", handlers.ListProductCategories(db))
	return r
}

// T02 — crear producto persiste category Y characteristics.
func TestCreateProduct_PersistsCategoryAndCharacteristics(t *testing.T) {
	db := setupCatDB(t)
	r := mountCatRouter(db, "t-cat")
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":              "c1000000-0000-4000-8000-000000000001",
		"name":            "Gaseosa",
		"price":           3000,
		"category":        "Gaseosas",
		"characteristics": "Sin azúcar\nMarca Nacional",
	})
	require.Equal(t, http.StatusCreated, w.Code)

	var p models.Product
	require.NoError(t, db.First(&p, "tenant_id = ?", "t-cat").Error)
	require.Equal(t, "Gaseosas", p.Category)
	require.Equal(t, "Sin azúcar\nMarca Nacional", p.Characteristics)
}

// T03 — editar persiste category/characteristics y NO los pisa cuando no se envían
// (invariante: no perder las categorías ya creadas).
func TestUpdateProduct_CategoryCharacteristics_NoWipe(t *testing.T) {
	db := setupCatDB(t)
	r := mountCatRouter(db, "t-cat")
	p := models.Product{
		BaseModel: models.BaseModel{ID: "c2000000-0000-4000-8000-000000000002"},
		TenantID:  "t-cat", Name: "Arroz", Price: 2000, Category: "Granos", Stock: 5,
	}
	require.NoError(t, db.Create(&p).Error)

	// PATCH solo characteristics → la categoría existente NO se pierde.
	w := doJSON(t, r, http.MethodPatch, "/products/"+p.ID, map[string]any{
		"characteristics": "Bulto 25kg",
	})
	require.Equal(t, http.StatusOK, w.Code)
	var got models.Product
	require.NoError(t, db.First(&got, "id = ?", p.ID).Error)
	require.Equal(t, "Granos", got.Category, "la categoría existente debe preservarse")
	require.Equal(t, "Bulto 25kg", got.Characteristics)

	// PATCH category → la cambia.
	w2 := doJSON(t, r, http.MethodPatch, "/products/"+p.ID, map[string]any{"category": "Abarrotes"})
	require.Equal(t, http.StatusOK, w2.Code)
	require.NoError(t, db.First(&got, "id = ?", p.ID).Error)
	require.Equal(t, "Abarrotes", got.Category)
	require.Equal(t, "Bulto 25kg", got.Characteristics, "characteristics no debe perderse al cambiar la categoría")
}

// T04 — GET /products/categories: distinct por tenant, por frecuencia, sin vacíos.
func TestListProductCategories_DistinctByFrequencyTenantScoped(t *testing.T) {
	db := setupCatDB(t)
	r := mountCatRouter(db, "t-cat")
	seed := func(id, tenant, cat string) {
		require.NoError(t, db.Create(&models.Product{
			BaseModel: models.BaseModel{ID: id}, TenantID: tenant, Name: id, Price: 1000, Category: cat,
		}).Error)
	}
	seed("p1", "t-cat", "Gaseosas")
	seed("p2", "t-cat", "Gaseosas")
	seed("p3", "t-cat", "Aseo")
	seed("p4", "t-cat", "")      // vacío → excluido
	seed("p5", "other", "Licor") // otro tenant → excluido

	w := doJSON(t, r, http.MethodGet, "/products/categories", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data []string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, []string{"Gaseosas", "Aseo"}, resp.Data)
}
