// Bug real reportado: los productos que create_product_screen.dart crea
// SOLO para probar fotos de IA ("Quitar fondo"/"Mejorar con IA"/"Crear foto
// con IA") antes de tocar "Guardar" quedaban indistinguibles de un producto
// real — aparecían en el inventario y en el autocompletado de "Nuevo
// Producto" (etiqueta "Mi tienda") aunque el tendero nunca guardara. Ver
// models.Product.IsDraft.
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

func setupProductDraftDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.InventoryMovement{}))
	return db
}

func mountDraftProductHandlers(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/products", handlers.CreateProduct(db, nil))
	r.PATCH("/products/:id", handlers.UpdateProduct(db, nil))
	r.GET("/products", handlers.ListProducts(db))
	return r
}

// CreateProduct debe persistir is_draft=true cuando el frontend lo manda
// explícitamente — es lo que hace _enhanceOrGeneratePhoto al crear el
// producto temporal para las pruebas de foto.
func TestCreateProduct_IsDraft_PersistsFlag(t *testing.T) {
	db := setupProductDraftDB(t)
	r := mountDraftProductHandlers(db, "tenant-draft")

	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":       "d1000000-0000-4000-8000-000000000001",
		"name":     "Producto temporal",
		"price":    1000,
		"is_draft": true,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", "d1000000-0000-4000-8000-000000000001").Error)
	assert.True(t, p.IsDraft, "el producto creado para probar fotos debe quedar marcado como borrador")
}

// Una creación normal (el flujo real de "Guardar" sin pasar por pruebas de
// IA) nunca debe quedar marcada como borrador — is_draft por defecto false.
func TestCreateProduct_WithoutIsDraft_DefaultsToFalse(t *testing.T) {
	db := setupProductDraftDB(t)
	r := mountDraftProductHandlers(db, "tenant-draft")

	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    "d1000000-0000-4000-8000-000000000002",
		"name":  "Arroz Diana",
		"price": 3200,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", "d1000000-0000-4000-8000-000000000002").Error)
	assert.False(t, p.IsDraft)
}

// El bug reportado exacto: un producto borrador NO debe aparecer en
// GET /products — ese es el endpoint que sincroniza a Isar y alimenta el
// autocompletado de "Nuevo Producto" ("Mi tienda") y el inventario/POS.
func TestListProducts_ExcludesDrafts(t *testing.T) {
	db := setupProductDraftDB(t)
	const tenant = "tenant-draft-list"

	require.NoError(t, db.Create(&models.Product{
		TenantID: tenant, Name: "Llavero Stitch (prueba sin guardar)",
		Price: 1000, IsAvailable: true, IsDraft: true,
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		TenantID: tenant, Name: "Coca-Cola 400ml",
		Price: 2500, IsAvailable: true, IsDraft: false,
	}).Error)

	r := mountDraftProductHandlers(db, tenant)
	names := listProductNames(t, r, "/products")

	assert.ElementsMatch(t, []string{"Coca-Cola 400ml"}, names,
		"el borrador nunca guardado no debe aparecer en el inventario ni en el autocompletado")
}

// Al confirmar "Guardar" (_save en create_product_screen.dart) sobre un
// producto que se creó como borrador durante las pruebas de foto, el PATCH
// debe poder poner is_draft=false y que el producto pase a ser visible.
func TestUpdateProduct_IsDraft_FlipsToFalseOnSave(t *testing.T) {
	db := setupProductDraftDB(t)
	const tenant = "tenant-draft-save"
	id := "d1000000-0000-4000-8000-000000000003"

	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: id},
		TenantID:  tenant, Name: "Llavero Stitch",
		Price: 1000, IsAvailable: true, IsDraft: true,
	}).Error)

	r := mountDraftProductHandlers(db, tenant)
	w := doJSON(t, r, http.MethodPatch, "/products/"+id, map[string]any{
		"is_draft": false,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	names := listProductNames(t, r, "/products")
	assert.Contains(t, names, "Llavero Stitch",
		"tras confirmar Guardar, el producto debe volverse visible")
}
