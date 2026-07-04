// Spec: specs/095-variantes-producto/spec.md
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

func setupVariantGroupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.ProductVariantGroup{},
		&models.InventoryMovement{}, &models.PurchaseOrder{}, &models.PurchaseOrderItem{}))
	return db
}

func mountVariantGroupHandlers(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/product-variant-groups", handlers.CreateVariantGroup(db))
	r.GET("/product-variant-groups", handlers.ListVariantGroups(db))
	r.GET("/product-variant-groups/:id", handlers.GetVariantGroup(db))
	r.PATCH("/product-variant-groups/:id", handlers.UpdateVariantGroup(db))
	r.DELETE("/product-variant-groups/:id", handlers.DeleteVariantGroup(db))
	r.POST("/products/:id/adopt-variant-group", handlers.AdoptProductToVariantGroup(db))
	r.POST("/product-variant-groups/:id/generate-combinations", handlers.GenerateVariantCombinations(db))
	r.DELETE("/products/:id", handlers.DeleteProduct(db, nil))
	r.PATCH("/products/:id", handlers.UpdateProduct(db, nil))
	r.POST("/products", handlers.CreateProduct(db, nil))
	return r
}

// T-18 — POST /api/v1/products persiste variant_group_id/variant_attributes
// (necesario para que el reintento offline del frontend, que reusa este
// mismo endpoint, no pierda el vínculo si el producto se creó sin conexión).
func TestCreateProduct_PersistsVariantFields(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.ProductVariantGroup{
		BaseModel: models.BaseModel{ID: "g1"}, TenantID: "t1", Name: "Camiseta",
	}).Error)

	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Camiseta Roja M", "price": 20000, "stock": 3,
		"variant_group_id":   "g1",
		"variant_attributes": `{"Talla":"M","Color":"Rojo"}`,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var products []models.Product
	require.NoError(t, db.Where("tenant_id = ?", "t1").Find(&products).Error)
	require.Len(t, products, 1)
	require.NotNil(t, products[0].VariantGroupID)
	assert.Equal(t, "g1", *products[0].VariantGroupID)
	assert.Equal(t, `{"Talla":"M","Color":"Rojo"}`, products[0].VariantAttributes)
}

// T-18b — un producto normal (sin campos de variante) sigue igual (AC-01).
func TestCreateProduct_WithoutVariantFields_Unaffected(t *testing.T) {
	db := setupVariantGroupDB(t)
	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Arroz Diana 500g", "price": 3200, "stock": 10,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var product models.Product
	require.NoError(t, db.Where("tenant_id = ?", "t1").First(&product).Error)
	assert.Nil(t, product.VariantGroupID)
	assert.Equal(t, "{}", product.VariantAttributes)
}

// T-08 — crear grupo scoped a tenant_id.
func TestCreateVariantGroup_Scoped(t *testing.T) {
	db := setupVariantGroupDB(t)
	r := mountVariantGroupHandlers(db, "t1")

	w := doJSON(t, r, http.MethodPost, "/product-variant-groups", map[string]any{
		"name": "Camiseta Básica", "category": "Ropa",
		"attribute_labels": []string{"Talla", "Color"},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var group models.ProductVariantGroup
	require.NoError(t, db.First(&group).Error)
	assert.Equal(t, "t1", group.TenantID)
	assert.Equal(t, "Camiseta Básica", group.Name)
}

// T-09 — IDOR: leer/editar un grupo de OTRO tenant → 404 (AC-08).
func TestVariantGroup_CrossTenant_404(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.ProductVariantGroup{
		BaseModel: models.BaseModel{ID: "g1"}, TenantID: "t1", Name: "Camiseta",
	}).Error)

	// Un atacante de otro tenant intenta leer/editar/borrar el grupo de t1.
	rAttacker := mountVariantGroupHandlers(db, "t2-atacante")

	wGet := doJSON(t, rAttacker, http.MethodGet, "/product-variant-groups/g1", nil)
	assert.Equal(t, http.StatusNotFound, wGet.Code, "GET cruzado debe dar 404, no 200")

	wPatch := doJSON(t, rAttacker, http.MethodPatch, "/product-variant-groups/g1",
		map[string]any{"name": "hackeado"})
	assert.Equal(t, http.StatusNotFound, wPatch.Code, "PATCH cruzado debe dar 404")

	wDelete := doJSON(t, rAttacker, http.MethodDelete, "/product-variant-groups/g1", nil)
	assert.Equal(t, http.StatusNotFound, wDelete.Code, "DELETE cruzado debe dar 404")

	// El dueño real sigue pudiendo verlo.
	rOwner := mountVariantGroupHandlers(db, "t1")
	wOwner := doJSON(t, rOwner, http.MethodGet, "/product-variant-groups/g1", nil)
	assert.Equal(t, http.StatusOK, wOwner.Code)
}

// T-10 — borrar un grupo con variantes vivas → 409 (AC-09).
func TestDeleteVariantGroup_WithLiveVariants_409(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.ProductVariantGroup{
		BaseModel: models.BaseModel{ID: "g1"}, TenantID: "t1", Name: "Camiseta",
	}).Error)
	gid := "g1"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Camiseta M",
		Price: 20000, Stock: 5, VariantGroupID: &gid,
	}).Error)

	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodDelete, "/product-variant-groups/g1", nil)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	// Sigue existiendo.
	var group models.ProductVariantGroup
	assert.NoError(t, db.First(&group, "id = ?", "g1").Error)
}

// T-10b — borrar la última variante deja el grupo borrable.
func TestDeleteVariantGroup_WithoutVariants_OK(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.ProductVariantGroup{
		BaseModel: models.BaseModel{ID: "g1"}, TenantID: "t1", Name: "Camiseta",
	}).Error)

	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodDelete, "/product-variant-groups/g1", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// T-12 — adopción: UPDATE in-place, preserva ProductID (AC-03).
func TestAdoptProductToVariantGroup_PreservesID(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.ProductVariantGroup{
		BaseModel: models.BaseModel{ID: "g1"}, TenantID: "t1", Name: "Camiseta",
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "existing-product"}, TenantID: "t1",
		Name: "Camiseta Roja Talla M", Price: 20000, Stock: 3,
	}).Error)
	// Simula historial de ventas ya existente en esa fila.
	require.NoError(t, db.Create(&models.InventoryMovement{
		ID: "mov1", TenantID: "t1", ProductID: "existing-product",
		MovementType: models.MovementInitialStock, Quantity: 3, StockBefore: 0, StockAfter: 3,
	}).Error)

	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodPost, "/products/existing-product/adopt-variant-group", map[string]any{
		"variant_group_id":  "g1",
		"variant_attributes": map[string]string{"Talla": "M", "Color": "Rojo"},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", "existing-product").Error)
	assert.Equal(t, "existing-product", product.ID, "la fila NO se recrea")
	require.NotNil(t, product.VariantGroupID)
	assert.Equal(t, "g1", *product.VariantGroupID)

	// El movimiento de kardex histórico sigue intacto y sigue apuntando a la
	// misma fila (AC-03: no se pierde historial).
	var mov models.InventoryMovement
	require.NoError(t, db.First(&mov, "id = ?", "mov1").Error)
	assert.Equal(t, "existing-product", mov.ProductID)
}

// T-14 — generar combinaciones crea N productos con stock/kardex inicial (AC-02, AC-04).
func TestGenerateVariantCombinations_CreatesProductsWithKardex(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.ProductVariantGroup{
		BaseModel: models.BaseModel{ID: "g1"}, TenantID: "t1", Name: "Camiseta",
	}).Error)

	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodPost, "/product-variant-groups/g1/generate-combinations", map[string]any{
		"attributes": map[string][]string{
			"Talla": {"S", "M"},
			"Color": {"Rojo", "Azul"},
		},
		"base_price": 20000,
		"base_stock": 5,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var products []models.Product
	require.NoError(t, db.Where("variant_group_id = ?", "g1").Find(&products).Error)
	assert.Len(t, products, 4, "2 tallas x 2 colores = 4 combinaciones")

	for _, p := range products {
		assert.Equal(t, float64(20000), p.Price)
		assert.Equal(t, 5, p.Stock)
		var n int64
		db.Model(&models.InventoryMovement{}).Where("product_id = ?", p.ID).Count(&n)
		assert.Equal(t, int64(1), n, "cada variante genera su propio movimiento de kardex inicial")
	}
}

// T-16 — desactivar/borrar un producto con PO abierta → bloqueado.
func TestDeleteProduct_WithOpenPurchaseOrder_Blocked(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Camiseta M", Price: 20000, Stock: 5,
	}).Error)
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel: models.BaseModel{ID: "po1"}, TenantID: "t1", SupplierID: "sup1",
		Status: models.PurchaseOrderSent,
	}).Error)
	pid := "p1"
	require.NoError(t, db.Create(&models.PurchaseOrderItem{
		BaseModel: models.BaseModel{ID: "poi1"}, PurchaseOrderID: "po1", ProductID: &pid,
		NameSnapshot: "Camiseta M", Quantity: 10, UnitCost: 10000,
	}).Error)

	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodDelete, "/products/p1", nil)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	var product models.Product
	assert.NoError(t, db.First(&product, "id = ?", "p1").Error, "el producto no debe borrarse")
}

// T-16b — sin PO abierta, el borrado procede normal.
func TestDeleteProduct_NoOpenPurchaseOrder_OK(t *testing.T) {
	db := setupVariantGroupDB(t)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Camiseta M", Price: 20000, Stock: 5,
	}).Error)

	r := mountVariantGroupHandlers(db, "t1")
	w := doJSON(t, r, http.MethodDelete, "/products/p1", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
