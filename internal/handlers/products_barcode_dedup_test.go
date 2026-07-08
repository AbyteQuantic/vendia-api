// Spec: specs/100-completar-skus-inventario/spec.md
//
// Spec 100 / D1 — dedup de barcode por tenant. Hoy dos productos del mismo
// tenant pueden quedar con el MISMO código de barras (UpdateProduct escribe
// barcode sin chequeo y el índice no es único): el POS cobra el producto
// equivocado. Estos tests cubren el contrato 409 `duplicate_barcode` en
// UpdateProduct (T-01) y CreateProduct (T-03):
//
//	{"error":"duplicate_barcode","message":"<español>",
//	 "existing_product":{"id","name","presentation"}}
//
// Reglas: mismo producto re-guardando su propio barcode → 200; otro tenant
// con el mismo barcode → no conflictúa (Art. III); barcode vacío → sin chequeo.
package handlers_test

import (
	"encoding/json"
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

func setupBarcodeDedupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.InventoryMovement{}))
	return db
}

func mountBarcodeDedupHandlers(db *gorm.DB, tenantID string) *gin.Engine {
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

func seedBarcodeProduct(t *testing.T, db *gorm.DB, id, tenantID, name, presentation, barcode string) models.Product {
	t.Helper()
	p := models.Product{
		BaseModel:    models.BaseModel{ID: id},
		TenantID:     tenantID,
		Name:         name,
		Presentation: presentation,
		Barcode:      barcode,
		Price:        1000,
		IsAvailable:  true,
	}
	require.NoError(t, db.Create(&p).Error)
	return p
}

func assertDuplicateBarcodeBody(t *testing.T, raw []byte, ownerID, ownerName, ownerPresentation string) {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Equal(t, "duplicate_barcode", body["error"])
	msg, _ := body["message"].(string)
	assert.NotEmpty(t, msg, "message en español para el tendero")

	owner, ok := body["existing_product"].(map[string]any)
	require.True(t, ok, "existing_product debe venir en el body: %s", string(raw))
	assert.Equal(t, ownerID, owner["id"])
	assert.Equal(t, ownerName, owner["name"])
	assert.Equal(t, ownerPresentation, owner["presentation"])
}

// ── T-01 — UpdateProduct ────────────────────────────────────────────────

func TestUpdateProduct_BarcodeOwnedByOtherProduct_Returns409(t *testing.T) {
	db := setupBarcodeDedupDB(t)
	r := mountBarcodeDedupHandlers(db, "tenant-sku")

	owner := seedBarcodeProduct(t, db,
		"aaaaaaaa-0000-4000-8000-000000000001", "tenant-sku",
		"Coca-Cola", "botella", "7701234567890")
	victim := seedBarcodeProduct(t, db,
		"aaaaaaaa-0000-4000-8000-000000000002", "tenant-sku",
		"Pony Malta", "botella", "")

	w := doJSON(t, r, http.MethodPatch, "/products/"+victim.ID, map[string]any{
		"barcode": "7701234567890",
	})
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assertDuplicateBarcodeBody(t, w.Body.Bytes(), owner.ID, owner.Name, owner.Presentation)

	// El barcode del producto NO debe haberse escrito.
	var after models.Product
	require.NoError(t, db.First(&after, "id = ?", victim.ID).Error)
	assert.Equal(t, "", after.Barcode, "nunca asignar el código en conflicto")
}

func TestUpdateProduct_SameProductResavesOwnBarcode_Returns200(t *testing.T) {
	db := setupBarcodeDedupDB(t)
	r := mountBarcodeDedupHandlers(db, "tenant-sku")

	p := seedBarcodeProduct(t, db,
		"bbbbbbbb-0000-4000-8000-000000000001", "tenant-sku",
		"Águila Light", "lata", "7709876543210")

	w := doJSON(t, r, http.MethodPatch, "/products/"+p.ID, map[string]any{
		"barcode": "7709876543210",
		"price":   3500,
	})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestUpdateProduct_SameBarcodeInOtherTenant_NoConflict(t *testing.T) {
	db := setupBarcodeDedupDB(t)
	r := mountBarcodeDedupHandlers(db, "tenant-a")

	// El barcode "999..." vive en OTRO tenant — no debe conflictuar (Art. III).
	seedBarcodeProduct(t, db,
		"cccccccc-0000-4000-8000-000000000001", "tenant-b",
		"Producto Ajeno", "caja", "9990001112223")
	mine := seedBarcodeProduct(t, db,
		"cccccccc-0000-4000-8000-000000000002", "tenant-a",
		"Producto Mío", "caja", "")

	w := doJSON(t, r, http.MethodPatch, "/products/"+mine.ID, map[string]any{
		"barcode": "9990001112223",
	})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Product
	require.NoError(t, db.First(&after, "id = ?", mine.ID).Error)
	assert.Equal(t, "9990001112223", after.Barcode)
}

func TestUpdateProduct_EmptyBarcode_SkipsCheck(t *testing.T) {
	db := setupBarcodeDedupDB(t)
	r := mountBarcodeDedupHandlers(db, "tenant-sku")

	// Dos productos sin código: escribir "" (o solo espacios) jamás
	// conflictúa — "sin código" no es una identidad.
	seedBarcodeProduct(t, db,
		"dddddddd-0000-4000-8000-000000000001", "tenant-sku",
		"Bolsa Arroz", "bolsa", "")
	p := seedBarcodeProduct(t, db,
		"dddddddd-0000-4000-8000-000000000002", "tenant-sku",
		"Bolsa Azúcar", "bolsa", "1112223334445")

	w := doJSON(t, r, http.MethodPatch, "/products/"+p.ID, map[string]any{
		"barcode": "   ",
	})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Product
	require.NoError(t, db.First(&after, "id = ?", p.ID).Error)
	assert.Equal(t, "", after.Barcode, "espacios se normalizan a vacío")
}

// ── T-03 — CreateProduct (mismo contrato) ───────────────────────────────

func TestCreateProduct_DuplicateBarcode_Returns409(t *testing.T) {
	db := setupBarcodeDedupDB(t)
	r := mountBarcodeDedupHandlers(db, "tenant-sku")

	owner := seedBarcodeProduct(t, db,
		"eeeeeeee-0000-4000-8000-000000000001", "tenant-sku",
		"Coca-Cola", "botella", "7701234567890")

	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Coca-Cola Zero", "presentation": "lata",
		"price": 3000, "stock": 5, "barcode": "7701234567890",
	})
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assertDuplicateBarcodeBody(t, w.Body.Bytes(), owner.ID, owner.Name, owner.Presentation)

	var count int64
	db.Model(&models.Product{}).Count(&count)
	assert.EqualValues(t, 1, count, "no debe crear una segunda fila")
}

func TestCreateProduct_SameBarcodeInOtherTenant_NoConflict(t *testing.T) {
	db := setupBarcodeDedupDB(t)
	r := mountBarcodeDedupHandlers(db, "tenant-a")

	seedBarcodeProduct(t, db,
		"ffffffff-0000-4000-8000-000000000001", "tenant-b",
		"Producto Ajeno", "caja", "9990001112223")

	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Producto Mío", "presentation": "caja",
		"price": 2000, "stock": 1, "barcode": "9990001112223",
	})
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

func TestCreateProduct_EmptyBarcode_SkipsCheck(t *testing.T) {
	db := setupBarcodeDedupDB(t)
	r := mountBarcodeDedupHandlers(db, "tenant-sku")

	seedBarcodeProduct(t, db,
		"abababab-0000-4000-8000-000000000001", "tenant-sku",
		"Bolsa Arroz", "bolsa", "")

	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Bolsa Azúcar", "presentation": "bolsa",
		"price": 4000, "stock": 2, "barcode": "",
	})
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

// El clasificador de violación del índice único (T-05) se prueba en
// services/product_barcode_test.go — el helper se movió a services porque
// también lo usan el importador CSV y el sync offline.
