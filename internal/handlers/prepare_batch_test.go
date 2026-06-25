// Spec: specs/080-platos-por-porciones/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func setupPrepareBatchDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{}, &models.Recipe{}, &models.RecipeIngredient{},
		&models.Ingredient{}, &models.InventoryMovement{},
	))
	return db
}

// AC-01: prepare-batch descuenta insumos de N porciones UNA vez, fija stock=N,
// marca por_porciones y prepared_date. AC-04: doble llamada el mismo día NO
// vuelve a descontar insumos (idempotente por anchor batch:{id}:{hoy}).
func TestPrepareDishBatch_DiscountsInsumosOnceAndSetsStock(t *testing.T) {
	db := setupPrepareBatchDB(t)
	const tenant = "tenant-pp"
	pid := "p0000000-0000-4000-8000-0000000000aa"
	rid := "r0000000-0000-4000-8000-0000000000aa"
	insID := "i0000000-0000-4000-8000-0000000000aa"

	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: insID}, TenantID: tenant,
		Name: "Arroz", Unit: models.UnitKg, Stock: 5, UnitCost: 2900,
	}).Error)
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel: models.BaseModel{ID: rid}, TenantID: tenant,
		ProductName: "Bandeja", SalePrice: 18000, ProductID: &pid,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: rid, ProductName: "Arroz", Quantity: 0.1, UnitCost: 2900, IngredientID: &insID},
		},
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: pid}, TenantID: tenant, Name: "Bandeja",
		Price: 18000, IsMenuItem: true, IsRecipe: true, RecipeID: &rid,
	}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenant); c.Next() })
	r.POST("/api/v1/products/:id/prepare-batch", handlers.PrepareDishBatch(db))

	call := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"portions": 20})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
			"/api/v1/products/"+pid+"/prepare-batch", bytes.NewReader(body)))
		return w
	}

	w := call()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", pid).Error)
	assert.Equal(t, "por_porciones", prod.AvailabilityMode)
	assert.Equal(t, 20, prod.Stock, "20 porciones listas")
	require.NotNil(t, prod.PreparedDate)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", insID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "5 - 20*0.1 = 3 (descuento de insumos del lote)")

	// Segunda llamada el mismo día: NO descuenta insumos de nuevo (idempotente).
	w2 := call()
	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())
	require.NoError(t, db.First(&arroz, "id = ?", insID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "doble preparado mismo día no re-descuenta insumos")
}

// Un plato incompleto (sin receta) no se puede cocinar por lote.
func TestPrepareDishBatch_IncompleteDish_400(t *testing.T) {
	db := setupPrepareBatchDB(t)
	const tenant = "tenant-pp2"
	pid := "p0000000-0000-4000-8000-0000000000bb"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: pid}, TenantID: tenant, Name: "Bagre Frito",
		Price: 46000, IsMenuItem: true, // sin receta
	}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenant); c.Next() })
	r.POST("/api/v1/products/:id/prepare-batch", handlers.PrepareDishBatch(db))

	body, _ := json.Marshal(map[string]any{"portions": 10})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/v1/products/"+pid+"/prepare-batch", bytes.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
