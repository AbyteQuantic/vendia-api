// Spec: specs/078-centro-tareas-unificado/spec.md
//
// sellable_only en GET /products: un plato de menú INCOMPLETO (is_menu_item sin
// receta con ingredientes) NO debe aparecer en el módulo de ventas (POS). El
// inventario (sin el flag) los sigue viendo para completarlos.
// Reporte del fundador 2026-06-24.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSellableDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	stmts := []string{
		`CREATE TABLE products (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT,
			name TEXT NOT NULL, price REAL NOT NULL DEFAULT 0,
			stock INTEGER NOT NULL DEFAULT 0,
			is_available INTEGER DEFAULT 1,
			is_menu_item INTEGER DEFAULT 0,
			requires_container INTEGER DEFAULT 0, container_price INTEGER DEFAULT 0,
			purchase_price REAL DEFAULT 0, is_price_pending INTEGER DEFAULT 0,
			barcode TEXT DEFAULT '', image_url TEXT DEFAULT '',
			presentation TEXT DEFAULT '', content TEXT DEFAULT '',
			ingestion_method TEXT DEFAULT 'manual', expiry_date DATETIME
		)`,
		`CREATE TABLE recipes (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, product_id TEXT
		)`,
		`CREATE TABLE recipe_ingredients (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, recipe_uuid TEXT NOT NULL
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

func seedSellableProduct(t *testing.T, db *gorm.DB, id, tenantID, name string, isMenuItem bool) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO products (id, created_at, updated_at, tenant_id, name, price, stock, is_available, is_menu_item)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		id, time.Now(), time.Now(), tenantID, name, 1000.0, 0, isMenuItem).Error)
}

func mountSellableProducts(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/api/v1/products", handlers.ListProducts(db))
	return r
}

func listProductNames(t *testing.T, r *gin.Engine, url string) []string {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	names := make([]string, 0, len(resp.Data))
	for _, d := range resp.Data {
		names = append(names, d.Name)
	}
	return names
}

func TestListProducts_SellableOnly_HidesIncompleteMenuItems(t *testing.T) {
	db := setupSellableDB(t)
	const tenant = "tenant-1"

	// a) Producto normal (no es plato).
	seedSellableProduct(t, db, "p-normal", tenant, "Coca-Cola", false)
	// b) Plato COMPLETO: is_menu_item + receta con 1 ingrediente.
	seedSellableProduct(t, db, "p-complete", tenant, "Mojarra Frita", true)
	require.NoError(t, db.Exec(
		`INSERT INTO recipes (id, created_at, updated_at, tenant_id, product_id) VALUES (?, ?, ?, ?, ?)`,
		"r-complete", time.Now(), time.Now(), tenant, "p-complete").Error)
	require.NoError(t, db.Exec(
		`INSERT INTO recipe_ingredients (id, created_at, updated_at, recipe_uuid) VALUES (?, ?, ?, ?)`,
		"ri-1", time.Now(), time.Now(), "r-complete").Error)
	// c) Plato INCOMPLETO: is_menu_item sin receta.
	seedSellableProduct(t, db, "p-incomplete", tenant, "Bagre Frito", true)

	r := mountSellableProducts(db, tenant)

	// Sin el flag (inventario): los 3 aparecen.
	all := listProductNames(t, r, "/api/v1/products")
	assert.ElementsMatch(t, []string{"Coca-Cola", "Mojarra Frita", "Bagre Frito"}, all)

	// Con sellable_only (POS): el plato incompleto NO aparece.
	sellable := listProductNames(t, r, "/api/v1/products?sellable_only=true")
	assert.ElementsMatch(t, []string{"Coca-Cola", "Mojarra Frita"}, sellable,
		"el plato incompleto (Bagre Frito) no debe estar en ventas")
	assert.NotContains(t, sellable, "Bagre Frito")
}

func TestListProducts_SellableOnly_NoCompleteDishes_HidesAllMenuItems(t *testing.T) {
	db := setupSellableDB(t)
	const tenant = "tenant-2"
	seedSellableProduct(t, db, "p-normal", tenant, "Arroz", false)
	seedSellableProduct(t, db, "p-dish", tenant, "Sancocho", true) // incompleto

	r := mountSellableProducts(db, tenant)
	sellable := listProductNames(t, r, "/api/v1/products?sellable_only=true")
	// Sin platos completos, todos los is_menu_item se ocultan; queda el normal.
	assert.ElementsMatch(t, []string{"Arroz"}, sellable)
}

// Un insumo borrado (soft-delete) NO debe contar el plato como completo.
func TestListProducts_SellableOnly_DeletedIngredientStaysIncomplete(t *testing.T) {
	db := setupSellableDB(t)
	const tenant = "tenant-3"
	seedSellableProduct(t, db, "p-dish", tenant, "Pechuga Asada", true)
	require.NoError(t, db.Exec(
		`INSERT INTO recipes (id, created_at, updated_at, tenant_id, product_id) VALUES (?, ?, ?, ?, ?)`,
		"r-1", time.Now(), time.Now(), tenant, "p-dish").Error)
	require.NoError(t, db.Exec(
		`INSERT INTO recipe_ingredients (id, created_at, updated_at, deleted_at, recipe_uuid) VALUES (?, ?, ?, ?, ?)`,
		"ri-del", time.Now(), time.Now(), time.Now(), "r-1").Error)

	r := mountSellableProducts(db, tenant)
	sellable := listProductNames(t, r, "/api/v1/products?sellable_only=true")
	assert.NotContains(t, sellable, "Pechuga Asada",
		"un insumo borrado no completa el plato")
}
