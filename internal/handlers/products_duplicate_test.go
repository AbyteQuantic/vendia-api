// Auditoría 2026-07-03 (concilio POS↔Inventario↔Kardex, seguimiento): un
// tenant real acumuló hasta 9 copias del mismo producto (mismo nombre y
// presentación, stocks contradictorios entre sí: [24,24,24,0,0,0,0,0,0]).
// Causas raíz identificadas: reintentar "Nuevo Producto" tras un guardado
// que pareció fallar (cada intento genera un UUID cliente nuevo, así que la
// idempotencia por id no lo atrapa), y el OCR de menú creando 2 platos
// cuando la carta trae precio de media porción/porción completa. Este
// archivo cubre el aviso de duplicado que CreateProduct ahora hace por
// nombre+presentación normalizados — mismo criterio de identidad que ya usa
// el importador CSV (Spec 027).
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

func postWithBranchHeader(t *testing.T, r *gin.Engine, path string, body any, branchID string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if branchID != "" {
		req.Header.Set("X-Branch", branchID)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func setupProductDuplicateDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.InventoryMovement{}))
	return db
}

func mountProductDuplicateHandlers(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/products", handlers.CreateProduct(db, nil))
	return r
}

func TestCreateProduct_DuplicateNameAndPresentation_Returns409(t *testing.T) {
	db := setupProductDuplicateDB(t)
	r := mountProductDuplicateHandlers(db, "tenant-dup")

	w1 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Águila Light", "presentation": "botella", "price": 3000, "stock": 10,
	})
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	// Segundo intento: mismo nombre+presentación, SIN id (nuevo UUID
	// cliente, como pasa al reabrir "Nuevo Producto" para reintentar).
	w2 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "águila LIGHT", "presentation": "Botella", "price": 3000, "stock": 5,
	})
	assert.Equal(t, http.StatusConflict, w2.Code, w2.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &body))
	assert.Equal(t, "duplicate_product", body["error_code"])
	require.NotNil(t, body["existing_product"])

	var count int64
	db.Model(&models.Product{}).Count(&count)
	assert.EqualValues(t, 1, count, "no debe crear una segunda fila")
}

func TestCreateProduct_SameNameDifferentPresentation_NotADuplicate(t *testing.T) {
	db := setupProductDuplicateDB(t)
	r := mountProductDuplicateHandlers(db, "tenant-dup")

	w1 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Águila", "presentation": "lata", "price": 2500, "stock": 10,
	})
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	// Presentación distinta ("botella" vs "lata") → es un SKU distinto de
	// verdad, no debe bloquearse.
	w2 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Águila", "presentation": "botella", "price": 3000, "stock": 5,
	})
	assert.Equal(t, http.StatusCreated, w2.Code, w2.Body.String())

	var count int64
	db.Model(&models.Product{}).Count(&count)
	assert.EqualValues(t, 2, count)
}

func TestCreateProduct_ForceCreate_BypassesDuplicateCheck(t *testing.T) {
	db := setupProductDuplicateDB(t)
	r := mountProductDuplicateHandlers(db, "tenant-dup")

	w1 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Producto Repetido", "presentation": "unidad", "price": 1000, "stock": 1,
	})
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	// El tendero confirmó explícitamente "crear de todas formas".
	w2 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Producto Repetido", "presentation": "unidad", "price": 1000, "stock": 1,
		"force_create": true,
	})
	assert.Equal(t, http.StatusCreated, w2.Code, w2.Body.String())

	var count int64
	db.Model(&models.Product{}).Count(&count)
	assert.EqualValues(t, 2, count)
}

func TestCreateProduct_DraftDoesNotTriggerOrGetBlockedByDuplicateCheck(t *testing.T) {
	db := setupProductDuplicateDB(t)
	r := mountProductDuplicateHandlers(db, "tenant-dup")

	// Un borrador de prueba de foto no cuenta como "ya existe" para el
	// intento real posterior, ni un borrador nuevo se bloquea contra otro
	// borrador — los borradores son descartables (models.Product.IsDraft).
	w1 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id": "d1000000-0000-4000-8000-000000000001",
		"name": "Llavero Stitch", "presentation": "unidad", "price": 15000,
		"is_draft": true,
	})
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	w2 := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"name": "Llavero Stitch", "presentation": "unidad", "price": 15000, "stock": 10,
	})
	assert.Equal(t, http.StatusCreated, w2.Code, w2.Body.String())

	var count int64
	db.Model(&models.Product{}).Count(&count)
	assert.EqualValues(t, 2, count)
}

func TestCreateProduct_DuplicateCheck_ScopedByBranch(t *testing.T) {
	db := setupProductDuplicateDB(t)
	require.NoError(t, db.AutoMigrate(&models.Branch{}))
	branchA := "aaaaaaaa-0000-4000-8000-000000000001"
	branchB := "bbbbbbbb-0000-4000-8000-000000000002"
	require.NoError(t, db.Create(&models.Branch{
		BaseModel: models.BaseModel{ID: branchA}, TenantID: "tenant-dup", Name: "Sede A",
	}).Error)
	require.NoError(t, db.Create(&models.Branch{
		BaseModel: models.BaseModel{ID: branchB}, TenantID: "tenant-dup", Name: "Sede B",
	}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, "tenant-dup")
		c.Set(middleware.BranchIDKey, c.GetHeader("X-Branch"))
		c.Next()
	})
	r.POST("/products", handlers.CreateProduct(db, nil))

	w1 := postWithBranchHeader(t, r, "/products", map[string]any{
		"name": "Cerveza Club Colombia", "presentation": "lata", "price": 3500, "stock": 10,
	}, branchA)
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	// Misma referencia pero en OTRA sede física — es un producto/stock
	// distinto de verdad (Spec 014, inventario por sede), no un duplicado.
	w2 := postWithBranchHeader(t, r, "/products", map[string]any{
		"name": "Cerveza Club Colombia", "presentation": "lata", "price": 3500, "stock": 20,
	}, branchB)
	assert.Equal(t, http.StatusCreated, w2.Code, w2.Body.String())
}
