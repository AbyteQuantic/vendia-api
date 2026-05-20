// Spec: specs/027-importador-inventario/spec.md
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupProductImportDB creates an in-memory SQLite database with the products table.
func setupProductImportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	return db
}

// productImportRouter builds a minimal Gin engine wired with ImportProducts.
// tenantID is injected directly (as auth middleware would do in production).
func productImportRouter(db *gorm.DB, tenantID string, isSuperAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/products/import", func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		if isSuperAdmin {
			c.Set("is_super_admin", true)
		}
		ImportProducts(db)(c)
	})
	return r
}

// postProductImport POSTs JSON to /products/import.
func postProductImport(r http.Handler, body any) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/products/import", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// postProductImportWithHeaders is like postProductImport but adds extra headers.
func postProductImportWithHeaders(r http.Handler, body any, headers map[string]string) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/products/import", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// parseProductImportResp is a convenience helper to unmarshal the response body.
func parseProductImportResp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// ── (a) Row sin name → failed ────────────────────────────────────────────────

func TestImportProducts_EmptyName_Failed(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "", "price": "2500"}},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])
	assert.Equal(t, float64(0), data["updated"])

	failed := data["failed"].([]any)
	require.Len(t, failed, 1)
	f := failed[0].(map[string]any)
	assert.Equal(t, float64(0), f["row_index"])
	assert.Contains(t, strings.ToLower(f["reason"].(string)), "nombre")
}

// ── (b) Row con price inválido → failed ──────────────────────────────────────

func TestImportProducts_InvalidPrice_Failed(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	cases := []struct {
		price string
		label string
	}{
		{"abc", "non-numeric"},
		{"0", "zero"},
		{"-100", "negative"},
		{"", "empty"},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			payload := map[string]any{
				"rows":           []map[string]any{{"name": "Producto", "price": c.price}},
				"dedup_strategy": "merge_by_barcode_then_name",
			}
			w := postProductImport(r, payload)
			assert.Equal(t, http.StatusOK, w.Code, "price=%q", c.price)
			resp := parseProductImportResp(t, w)
			data := resp["data"].(map[string]any)
			assert.Equal(t, float64(0), data["created"], "price=%q should not create", c.price)
			failed := data["failed"].([]any)
			require.Len(t, failed, 1, "price=%q should fail", c.price)
		})
	}
}

// ── (c) Row nuevo con barcode → INSERT con ingestion_method='import' ─────────

func TestImportProducts_NewRowWithBarcode_Created(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{
				"name":    "Coca Cola 350ml",
				"price":   "2500",
				"barcode": "7702536001234",
				"stock":   "50",
			},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])
	assert.Equal(t, float64(0), data["updated"])

	// Verify invariants on the DB row
	var product models.Product
	require.NoError(t, db.Where("barcode = ? AND tenant_id = ?", "7702536001234", "tenant-1").First(&product).Error)
	assert.Equal(t, "import", product.IngestionMethod)
	assert.False(t, product.IsAIEnhanced, "is_ai_enhanced must be false on import")
	assert.Equal(t, float64(2500), product.Price)
	assert.Equal(t, 50, product.Stock)
}

// ── (d) Barcode existente → UPDATE, no toca campos protegidos ────────────────

func TestImportProducts_ExistingBarcode_Updated(t *testing.T) {
	db := setupProductImportDB(t)

	// Seed existing product with protected fields set
	existing := models.Product{
		BaseModel:       models.BaseModel{ID: "prod-uuid-1"},
		TenantID:        "tenant-1",
		Name:            "Old Name",
		Price:           1000,
		Barcode:         "1234567890123",
		Stock:           100,
		IsAIEnhanced:    true,  // must NOT be overwritten
		PhotoURL:        "https://old-photo.jpg",
		IngestionMethod: "manual",
	}
	require.NoError(t, db.Create(&existing).Error)

	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{
				"name":    "New Name",
				"price":   "2500",
				"barcode": "1234567890123",
				"stock":   "200",
			},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])
	assert.Equal(t, float64(1), data["updated"])

	// Verify update applied mutable fields
	var product models.Product
	require.NoError(t, db.Where("id = ?", "prod-uuid-1").First(&product).Error)
	assert.Equal(t, "New Name", product.Name)
	assert.Equal(t, float64(2500), product.Price)
	assert.Equal(t, 200, product.Stock)

	// Protected fields must NOT be touched
	assert.True(t, product.IsAIEnhanced, "is_ai_enhanced must not be overwritten")
	assert.Equal(t, "https://old-photo.jpg", product.PhotoURL, "photo_url must not be overwritten")
}

// ── (e) Sin barcode, name normalizado matchea → UPDATE por name fallback ─────

func TestImportProducts_NoBarcode_NameFallbackUpdate(t *testing.T) {
	db := setupProductImportDB(t)

	// Seed existing product without barcode
	existing := models.Product{
		BaseModel: models.BaseModel{ID: "prod-uuid-2"},
		TenantID:  "tenant-1",
		Name:      "Coca Cola",
		Price:     2000,
	}
	require.NoError(t, db.Create(&existing).Error)

	r := productImportRouter(db, "tenant-1", false)

	// Row with same name (with accent variant that normalizes the same)
	payload := map[string]any{
		"rows": []map[string]any{
			{
				"name":  "  Coca Cola  ", // extra spaces — normalizes to "coca cola"
				"price": "2500",
			},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])
	assert.Equal(t, float64(1), data["updated"])

	var product models.Product
	require.NoError(t, db.Where("id = ?", "prod-uuid-2").First(&product).Error)
	assert.Equal(t, float64(2500), product.Price)
}

// ── (f) Idempotencia: mismo lote dos veces → segunda vez 0 created ───────────

func TestImportProducts_Idempotent(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Leche Entera", "price": "3500", "barcode": "1111111111111"},
			{"name": "Pan Tajado", "price": "5000", "barcode": "2222222222222"},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	// First run
	w1 := postProductImport(r, payload)
	require.Equal(t, http.StatusOK, w1.Code)
	resp1 := parseProductImportResp(t, w1)
	data1 := resp1["data"].(map[string]any)
	assert.Equal(t, float64(2), data1["created"])
	assert.Equal(t, float64(0), data1["updated"])

	// Second run — same payload
	w2 := postProductImport(r, payload)
	require.Equal(t, http.StatusOK, w2.Code)
	resp2 := parseProductImportResp(t, w2)
	data2 := resp2["data"].(map[string]any)
	assert.Equal(t, float64(0), data2["created"], "second run must not duplicate")
	assert.Equal(t, float64(2), data2["updated"])
}

// ── (g) > 100 rows → 400 ─────────────────────────────────────────────────────

func TestImportProducts_TooManyRows_400(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	rows := make([]map[string]any, 101)
	for i := range rows {
		rows[i] = map[string]any{"name": "Producto", "price": "1000"}
	}

	payload := map[string]any{
		"rows":           rows,
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── (h) dedup_strategy inválido → 400 ────────────────────────────────────────

func TestImportProducts_InvalidDedupStrategy_400(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "Producto", "price": "1000"}},
		"dedup_strategy": "skip_duplicates",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestImportProducts_EmptyDedupStrategy_400(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{{"name": "Producto", "price": "1000"}},
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── (i) Stock decimal "1.5" → guardado como 2 (redondeo) ─────────────────────

func TestImportProducts_DecimalStock_RoundedToInt(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Producto Redondeo", "price": "1000", "stock": "1.5"},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])

	var product models.Product
	require.NoError(t, db.Where("name = ? AND tenant_id = ?", "Producto Redondeo", "tenant-1").First(&product).Error)
	assert.Equal(t, 2, product.Stock, "1.5 should round to 2")
}

// ── (j) Stock negativo → failed ───────────────────────────────────────────────

func TestImportProducts_NegativeStock_Failed(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Producto", "price": "1000", "stock": "-5"},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(0), data["created"])
	failed := data["failed"].([]any)
	require.Len(t, failed, 1)
	f := failed[0].(map[string]any)
	assert.Contains(t, strings.ToLower(f["reason"].(string)), "stock")
}

// ── Mixed batch: válidos e inválidos ─────────────────────────────────────────

func TestImportProducts_MixedBatch(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Válido 1", "price": "1000"},
			{"name": "", "price": "1000"},          // invalid name
			{"name": "Válido 2", "price": "abc"},   // invalid price
			{"name": "Válido 3", "price": "3000"},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(2), data["created"])
	assert.Equal(t, float64(2), float64(len(data["failed"].([]any))))
}

// ── God-mode: X-Tenant-Override sin super_admin → 403 ────────────────────────

func TestImportProducts_TenantOverride_NoSuperAdmin_403(t *testing.T) {
	db := setupProductImportDB(t)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/products/import", func(c *gin.Context) {
		c.Set("tenant_id", "tenant-regular")
		// No super-admin flag
		ImportProducts(db)(c)
	})

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "Test", "price": "1000"}},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImportWithHeaders(router, payload, map[string]string{
		"X-Tenant-Override": "other-tenant-uuid",
	})
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// ── God-mode: X-Tenant-Override con super_admin → usa override tenant ─────────

func TestImportProducts_TenantOverride_SuperAdmin_OK(t *testing.T) {
	db := setupProductImportDB(t)
	targetTenantID := "target-tenant-uuid"

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/products/import", func(c *gin.Context) {
		c.Set("tenant_id", "super-admin-tenant")
		c.Set("is_super_admin", true)
		ImportProducts(db)(c)
	})

	payload := map[string]any{
		"rows":           []map[string]any{{"name": "God Mode Product", "price": "5000"}},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImportWithHeaders(router, payload, map[string]string{
		"X-Tenant-Override": targetTenantID,
	})
	assert.Equal(t, http.StatusOK, w.Code)

	// Product must be under the override tenant
	var product models.Product
	require.NoError(t, db.Where("name = ? AND tenant_id = ?", "God Mode Product", targetTenantID).First(&product).Error)
	assert.Equal(t, "import", product.IngestionMethod)
}

// ── Price normalizado en formato COP "$ 1.500" → 1500 ────────────────────────

func TestImportProducts_COPPriceFormat_Parsed(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "Producto COP", "price": "$ 1.500"},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseProductImportResp(t, w)
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["created"])

	var product models.Product
	require.NoError(t, db.Where("name = ? AND tenant_id = ?", "Producto COP", "tenant-1").First(&product).Error)
	assert.Equal(t, float64(1500), product.Price)
}

// ── Whitespace sanitization en name ──────────────────────────────────────────

func TestImportProducts_WhitespaceSanitization(t *testing.T) {
	db := setupProductImportDB(t)
	r := productImportRouter(db, "tenant-1", false)

	payload := map[string]any{
		"rows": []map[string]any{
			{"name": "  Leche   Entera  ", "price": "3500"},
		},
		"dedup_strategy": "merge_by_barcode_then_name",
	}

	w := postProductImport(r, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var product models.Product
	require.NoError(t, db.Where("tenant_id = ?", "tenant-1").First(&product).Error)
	assert.Equal(t, "Leche Entera", product.Name)
}
