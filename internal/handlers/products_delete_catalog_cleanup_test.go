// Pregunta real del tendero: al borrar un producto "borrador" (creado solo
// para probar fotos de IA, sin ninguna venta), ¿deja de sugerirse a otros
// tenants en el catálogo compartido? ¿se borra el archivo del bucket? Hoy
// no — DeleteProduct solo tocaba la fila `products`, sin ningún vínculo con
// catalog_images ni con el storage. Regla de negocio del fundador: un
// producto SIN ventas es casi siempre una referencia mal creada — al
// borrarlo, también se limpia SU contribución al catálogo compartido y el
// archivo real en R2.
package handlers_test

import (
	"context"
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

// doJSON is defined once in branches_test.go (same package handlers_test)
// and reused across handler test files.

// deleteTrackingStorage es un FileStorage falso que solo registra qué
// bucket/key le pidieron borrar — lo mínimo para afirmar que DeleteProduct
// sí (o no) intentó borrar el archivo real.
type deleteTrackingStorage struct {
	deleted []string // "bucket/key" por cada llamada a Delete
}

func (f *deleteTrackingStorage) Upload(context.Context, string, string, []byte, string) (string, error) {
	return "", nil
}
func (f *deleteTrackingStorage) Download(context.Context, string, string) ([]byte, string, error) {
	return nil, "", nil
}
func (f *deleteTrackingStorage) Delete(_ context.Context, bucket, key string) error {
	f.deleted = append(f.deleted, bucket+"/"+key)
	return nil
}

// setupProductDeleteCleanupDB hand-crafts catalog_images: its `id` column
// defaults to Postgres' gen_random_uuid(), which SQLite's AutoMigrate can't
// parse (same workaround used for the tenants table elsewhere in this
// package) — tests below always set ID explicitly, so no default is needed.
func setupProductDeleteCleanupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{}, &models.InventoryMovement{}, &models.SaleItem{},
	))
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_images (
			id TEXT PRIMARY KEY, catalog_product_id TEXT NOT NULL,
			image_url TEXT NOT NULL, storage_key TEXT NOT NULL,
			created_by_tenant_id TEXT NOT NULL, is_accepted BOOLEAN DEFAULT false,
			created_at DATETIME, updated_at DATETIME
		);
	`).Error)
	return db
}

func mountDeleteProduct(db *gorm.DB, storage *deleteTrackingStorage, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.DELETE("/products/:id", handlers.DeleteProduct(db, storage))
	return r
}

// El caso real reportado: un llavero de prueba sin ventas, cuya foto ya
// quedó registrada (y aceptada) en el catálogo compartido por ESTE mismo
// tenant — al borrar el producto, la contribución al catálogo y el archivo
// en R2 también deben desaparecer.
func TestDeleteProduct_NoSales_CleansOwnCatalogContributionAndR2(t *testing.T) {
	db := setupProductDeleteCleanupDB(t)
	storage := &deleteTrackingStorage{}
	const tenant = "tenant-sin-ventas"
	const photoURL = "https://fake-cdn.example/product-photos/products/tenant-sin-ventas/p1-enhanced.jpg"

	require.NoError(t, db.Create(&models.Product{
		TenantID: tenant, Name: "Llavero Stitch (prueba)",
		Price: 1000, PhotoURL: photoURL,
	}).Error)
	var product models.Product
	require.NoError(t, db.Where("tenant_id = ?", tenant).First(&product).Error)

	require.NoError(t, db.Create(&models.CatalogImage{
		ID: "img-1", CatalogProductID: "cat-1", ImageURL: photoURL,
		StorageKey: "products/tenant-sin-ventas/p1-enhanced.jpg",
		CreatedByTenantID: tenant, IsAccepted: true,
	}).Error)

	r := mountDeleteProduct(db, storage, tenant)
	w := doJSON(t, r, http.MethodDelete, "/products/"+product.ID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var count int64
	db.Model(&models.CatalogImage{}).Where("image_url = ?", photoURL).Count(&count)
	assert.Zero(t, count, "la contribución de este tenant al catálogo compartido debe borrarse")
	assert.Contains(t, storage.deleted, "product-photos/products/tenant-sin-ventas/p1-enhanced.jpg",
		"el archivo real debe borrarse del bucket, no quedar huérfano")
}

// Si el producto SÍ tiene al menos una venta, la foto se queda en el
// catálogo compartido — no es "una referencia mal creada", es un producto
// real que otros tenants pueden seguir viendo como sugerencia legítima.
func TestDeleteProduct_HasSales_KeepsCatalogImageAndFile(t *testing.T) {
	db := setupProductDeleteCleanupDB(t)
	storage := &deleteTrackingStorage{}
	const tenant = "tenant-con-ventas"
	const photoURL = "https://fake-cdn.example/product-photos/products/tenant-con-ventas/p2-enhanced.jpg"

	require.NoError(t, db.Create(&models.Product{
		TenantID: tenant, Name: "Coca-Cola 400ml",
		Price: 2500, PhotoURL: photoURL,
	}).Error)
	var product models.Product
	require.NoError(t, db.Where("tenant_id = ?", tenant).First(&product).Error)

	require.NoError(t, db.Create(&models.SaleItem{ProductID: &product.ID}).Error)
	require.NoError(t, db.Create(&models.CatalogImage{
		ID: "img-2", CatalogProductID: "cat-2", ImageURL: photoURL,
		StorageKey: "products/tenant-con-ventas/p2-enhanced.jpg",
		CreatedByTenantID: tenant, IsAccepted: true,
	}).Error)

	r := mountDeleteProduct(db, storage, tenant)
	w := doJSON(t, r, http.MethodDelete, "/products/"+product.ID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var count int64
	db.Model(&models.CatalogImage{}).Where("image_url = ?", photoURL).Count(&count)
	assert.Equal(t, int64(1), count, "un producto con ventas reales SÍ mantiene su foto en el catálogo compartido")
	assert.Empty(t, storage.deleted, "no se borra ningún archivo cuando el producto tuvo ventas")
}

// Nunca se toca la contribución de OTRO tenant, aunque comparta la misma
// URL de imagen (ej. dos tenants con la misma foto de catálogo genérico).
func TestDeleteProduct_NoSales_NeverTouchesOtherTenantsContribution(t *testing.T) {
	db := setupProductDeleteCleanupDB(t)
	storage := &deleteTrackingStorage{}
	const tenant = "tenant-a"
	const otherTenant = "tenant-b"
	const photoURL = "https://fake-cdn.example/product-photos/shared/generic.jpg"

	require.NoError(t, db.Create(&models.Product{
		TenantID: tenant, Name: "Llavero genérico (prueba)",
		Price: 1000, PhotoURL: photoURL,
	}).Error)
	var product models.Product
	require.NoError(t, db.Where("tenant_id = ?", tenant).First(&product).Error)

	require.NoError(t, db.Create(&models.CatalogImage{
		ID: "img-3", CatalogProductID: "cat-3", ImageURL: photoURL,
		StorageKey: "shared/generic.jpg",
		CreatedByTenantID: otherTenant, IsAccepted: true,
	}).Error)

	r := mountDeleteProduct(db, storage, tenant)
	w := doJSON(t, r, http.MethodDelete, "/products/"+product.ID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var count int64
	db.Model(&models.CatalogImage{}).Where("image_url = ?", photoURL).Count(&count)
	assert.Equal(t, int64(1), count, "la contribución de OTRO tenant nunca se borra")
	assert.Empty(t, storage.deleted)
}
