// Spec: specs/059-admin-tenant-activity/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// SQLite a mano: el modelo Tenant no migra en SQLite (default
// gen_random_uuid), así que armamos tablas estrechas con solo las
// columnas que el handler lee. `First(&tenant)` hace SELECT * y mapea
// las columnas presentes; los campos sin columna quedan en su zero-value.
func setupActivitySQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			owner_name TEXT, phone TEXT, business_name TEXT,
			business_types TEXT DEFAULT '[]', address TEXT DEFAULT '',
			store_slug TEXT, logo_url TEXT DEFAULT '',
			feature_flags TEXT DEFAULT '{}', enable_recipes INTEGER DEFAULT 0
		);`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE products (
			id TEXT PRIMARY KEY, tenant_id TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			name TEXT, price REAL DEFAULT 0, purchase_price REAL DEFAULT 0,
			stock INTEGER DEFAULT 0, min_stock INTEGER DEFAULT 0,
			ingestion_method TEXT DEFAULT 'manual'
		);`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE sales (
			id TEXT PRIMARY KEY, tenant_id TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			total REAL DEFAULT 0
		);`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE sale_items (
			id TEXT PRIMARY KEY, sale_id TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			product_id TEXT, name TEXT, price REAL, quantity INTEGER, subtotal REAL
		);`).Error)
	return db
}

func TestAdminTenantActivity_AggregatesStockSalesAndProfit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupActivitySQLite(t)

	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, created_at, owner_name, phone, business_name, store_slug, logo_url, enable_recipes)
		VALUES ('t1', datetime('now'), 'Ana', '300', 'Tienda Ana', 'tienda-ana', 'https://cdn/logo.png', 1)`).Error)

	// 4 productos: A (más vendido), D (sin costo → ganancia desconocida),
	// B (agotado, vendido 1), C (sin ventas).
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, tenant_id, name, price, purchase_price, stock, min_stock, ingestion_method) VALUES
			('pA','t1','Arroz', 300, 100, 5, 2, 'manual'),
			('pB','t1','Aceite', 100, 50, 0, 2, 'ia_factura'),
			('pC','t1','Atún', 200, 80, 10, 2, 'import'),
			('pD','t1','Pan', 150, 0, 7, 2, 'manual')`).Error)

	require.NoError(t, db.Exec(`INSERT INTO sales (id, tenant_id, total, created_at) VALUES ('s1','t1',880, datetime('now'))`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO sale_items (id, sale_id, product_id, name, price, quantity, subtotal) VALUES
			('i1','s1','pA','Arroz', 200, 3, 600),
			('i2','s1','pB','Aceite', 80, 1, 80),
			('i3','s1','pD','Pan', 100, 2, 200)`).Error)

	r := gin.New()
	r.GET("/api/v1/admin/tenants/:id/activity", handlers.AdminTenantActivity(db))
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/admin/tenants/t1/activity", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		LogoURL    string  `json:"logo_url"`
		CatalogURL string  `json:"catalog_url"`
		StoreSlug  *string `json:"store_slug"`
		Summary    struct {
			TotalProducts   int     `json:"total_products"`
			InStock         int     `json:"in_stock"`
			OutOfStock      int     `json:"out_of_stock"`
			TotalUnitsSold  int     `json:"total_units_sold"`
			TotalRevenue    float64 `json:"total_revenue"`
			EstimatedProfit float64 `json:"estimated_profit"`
			ActiveModules   int     `json:"active_modules"`
		} `json:"summary"`
		Products []struct {
			ID          string  `json:"id"`
			UnitsSold   int     `json:"units_sold"`
			Revenue     float64 `json:"revenue"`
			Profit      float64 `json:"profit"`
			ProfitKnown bool    `json:"profit_known"`
		} `json:"products"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	// Logo + link del catálogo.
	assert.Equal(t, "https://cdn/logo.png", body.LogoURL)
	assert.Contains(t, body.CatalogURL, "tienda-ana")

	// Resumen.
	assert.Equal(t, 4, body.Summary.TotalProducts)
	assert.Equal(t, 6, body.Summary.TotalUnitsSold) // 3 + 1 + 2
	assert.Equal(t, 880.0, body.Summary.TotalRevenue)
	// Ganancia: A 600-100*3=300 ; B 80-50*1=30 ; D sin costo (excluido) ; C 0 → 330.
	assert.Equal(t, 330.0, body.Summary.EstimatedProfit)
	assert.Equal(t, 1, body.Summary.OutOfStock)    // B stock 0
	assert.Equal(t, 1, body.Summary.ActiveModules) // recetas

	// Orden por frecuencia: A (3) primero, C (0) último.
	require.Len(t, body.Products, 4)
	assert.Equal(t, "pA", body.Products[0].ID)
	assert.Equal(t, 3, body.Products[0].UnitsSold)
	assert.Equal(t, 300.0, body.Products[0].Profit)
	assert.True(t, body.Products[0].ProfitKnown)
	assert.Equal(t, "pC", body.Products[3].ID)
	assert.Equal(t, 0, body.Products[3].UnitsSold)

	// Producto sin costo → ganancia desconocida, no infla el total.
	byID := map[string]bool{}
	for _, p := range body.Products {
		byID[p.ID] = p.ProfitKnown
	}
	assert.False(t, byID["pD"], "producto sin costo: profit_known=false")
}
