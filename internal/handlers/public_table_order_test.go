// Spec: specs/083-mesas-catalogo-qr/spec.md
//
// Council E2E (Spec 083) — pedido de mesa por QR UNIFICADO con la cuenta de mesa.
// Valida la matemática de cuentas (total = suma exacta de líneas, sin drift, sin
// filas duplicadas) y de inventario (no descuenta al abrir; descuenta exacto al
// cobrar; cancelar una cuenta abierta NO infla el stock).
package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// sharedDBSeq da un nombre único a cada BD compartida (soporta go test -count=N).
var sharedDBSeq int64

func setupPublicTableOrderDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	applyMesaSchema(t, db)
	return db
}

// setupSharedMesaDB abre una BD sqlite en memoria COMPARTIDA entre conexiones
// (cache=shared) para poder ejercer concurrencia real con goroutines. busy_timeout
// evita SQLITE_BUSY espurios. El nombre lo deriva del test para aislar.
func setupSharedMesaDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:mesa_%s_%d?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate",
		strings.ReplaceAll(t.Name(), "/", "_"), atomic.AddInt64(&sharedDBSeq, 1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	applyMesaSchema(t, db)
	return db
}

func applyMesaSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '', store_slug TEXT DEFAULT '',
			phone TEXT DEFAULT '', created_at DATETIME
		)`,
		`CREATE TABLE branches (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL, address TEXT DEFAULT '', is_active INTEGER DEFAULT 1
		)`,
		`CREATE TABLE products (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT,
			name TEXT NOT NULL, price REAL NOT NULL DEFAULT 0,
			stock INTEGER NOT NULL DEFAULT 0, is_available INTEGER DEFAULT 1,
			is_recipe INTEGER DEFAULT 0, recipe_id TEXT
		)`,
		`CREATE TABLE sales (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT,
			created_by TEXT, employee_uuid TEXT, employee_name TEXT DEFAULT '',
			receipt_number INTEGER DEFAULT 0, total REAL NOT NULL DEFAULT 0,
			tax_amount REAL DEFAULT 0, tip_amount REAL DEFAULT 0,
			payment_method TEXT NOT NULL, customer_id TEXT,
			customer_name_snapshot TEXT DEFAULT '', customer_phone_snapshot TEXT DEFAULT '',
			is_credit INTEGER DEFAULT 0, credit_account_id TEXT,
			payment_status TEXT DEFAULT 'COMPLETED', dynamic_qr_payload TEXT,
			source TEXT NOT NULL DEFAULT 'POS', receipt_image_url TEXT DEFAULT '',
			price_tier TEXT NOT NULL DEFAULT 'retail', quote_id TEXT,
			cost_amount REAL DEFAULT 0, event_registration_id TEXT
		)`,
		`CREATE TABLE sale_items (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, sale_id TEXT NOT NULL, product_id TEXT,
			name TEXT NOT NULL, price REAL NOT NULL DEFAULT 0, quantity INTEGER NOT NULL,
			subtotal REAL NOT NULL DEFAULT 0, is_container_charge INTEGER DEFAULT 0,
			is_service INTEGER DEFAULT 0, custom_description TEXT DEFAULT '',
			custom_unit_price REAL DEFAULT 0,
			employee_uuid TEXT, employee_name TEXT DEFAULT '', pay_basis TEXT DEFAULT 'none', commission_pct REAL, commission_amount REAL DEFAULT 0
		)`,
		// gen_random_uuid() default rompe AutoMigrate en sqlite → DDL crudo.
		`CREATE TABLE notifications (
			id TEXT PRIMARY KEY, created_at DATETIME, tenant_id TEXT NOT NULL,
			title TEXT NOT NULL, body TEXT DEFAULT '', type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0, deep_link TEXT, pushed_at DATETIME, dedup_key TEXT
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	require.NoError(t, db.AutoMigrate(
		&models.OrderTicket{}, &models.OrderItem{},
		&models.Recipe{}, &models.RecipeIngredient{}, &models.Ingredient{},
		&models.InventoryMovement{}, &models.Table{},
	))
	// Spec 083 — índice único parcial: una sola cuenta ABIERTA por (tenant,label).
	require.NoError(t, db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS uniq_open_table_account
		 ON order_tickets (tenant_id, label)
		 WHERE status IN ('nuevo','preparando','listo') AND deleted_at IS NULL`).Error)
}

func seedTenantSlug(t *testing.T, db *gorm.DB, slug string) (tenantID, branchID string) {
	t.Helper()
	tenantID = uuid.NewString()
	branchID = uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Rest', ?, ?)`,
		tenantID, slug, time.Now()).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO branches (id, tenant_id, name, is_active, created_at) VALUES (?, ?, 'Sede', 1, ?)`,
		branchID, tenantID, time.Now().Add(-time.Hour)).Error)
	return tenantID, branchID
}

func seedTbl(t *testing.T, db *gorm.DB, tenantID, label, area string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.Table{
		BaseModel: models.BaseModel{ID: id},
		TenantID:  tenantID, Label: label, Area: area, IsActive: true,
	}).Error)
	return id
}

func seedDirectProduct(t *testing.T, db *gorm.DB, tenantID, branchID, name string, price float64, stock int) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id, name, price, stock, is_available, is_recipe)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0)`,
		id, time.Now(), time.Now(), tenantID, branchID, name, price, stock).Error)
	return id
}

func mountPublicTableOrder(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/public/catalog/:slug/table/:id/order", handlers.PublicAddItemsToTableTab(db))
	return r
}

func postTableOrder(t *testing.T, r *gin.Engine, slug, tableID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, r, http.MethodPost,
		"/api/v1/public/catalog/"+slug+"/table/"+tableID+"/order", body)
}

func openOrderForLabel(t *testing.T, db *gorm.DB, tenantID, label string) models.OrderTicket {
	t.Helper()
	var ticket models.OrderTicket
	require.NoError(t, db.Preload("Items").
		Where("tenant_id = ? AND label = ? AND status IN ?", tenantID, label,
			[]string{"nuevo", "preparando", "listo"}).
		First(&ticket).Error)
	return ticket
}

func ticketLineSum(o models.OrderTicket) float64 {
	var s float64
	for _, it := range o.Items {
		s += it.UnitPrice * float64(it.Quantity)
	}
	return s
}

// ACC-OPEN-01 — abrir cuenta por QR: total = suma exacta y NO mueve inventario.
func TestPublicTableOrder_Open_TotalExactNoInventory(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "tienda")
	tableID := seedTbl(t, db, tenantID, "Mesa 1", "Terraza")
	pA := seedDirectProduct(t, db, tenantID, branchID, "Gaseosa", 2500, 10)
	pB := seedDirectProduct(t, db, tenantID, branchID, "Empanada", 1500, 10)
	r := mountPublicTableOrder(db)

	w := postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{
			{"product_id": pA, "name": "Gaseosa", "quantity": 2, "price": 2500},
			{"product_id": pB, "name": "Empanada", "quantity": 3, "price": 1500},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	o := openOrderForLabel(t, db, tenantID, "Mesa 1")
	assert.Equal(t, models.OrderStatusNuevo, o.Status)
	assert.Equal(t, models.OrderTypeMesa, o.Type)
	require.NotNil(t, o.BranchID)
	assert.Equal(t, 9500.0, o.Total)             // 2*2500 + 3*1500
	assert.Equal(t, o.Total, ticketLineSum(o))   // total == suma de líneas
	assert.Len(t, o.Items, 2)

	// Inventario intacto al abrir (solo se descuenta al cobrar).
	var sa, sb models.Product
	db.First(&sa, "id = ?", pA)
	db.First(&sb, "id = ?", pB)
	assert.Equal(t, 10, sa.Stock)
	assert.Equal(t, 10, sb.Stock)
	var movs int64
	db.Model(&models.InventoryMovement{}).Count(&movs)
	assert.Equal(t, int64(0), movs, "abrir cuenta no debe registrar kardex")

	// Solo una cuenta abierta; session_token presente.
	var n int64
	db.Model(&models.OrderTicket{}).Where("tenant_id = ? AND label = ?", tenantID, "Mesa 1").Count(&n)
	assert.Equal(t, int64(1), n)
	assert.NotEmpty(t, o.SessionToken)
}

// ACC-ADD-02 / ACC-TOKEN-27 — un 2º pedido cae en la MISMA cuenta y el token no cambia.
func TestPublicTableOrder_AddToSameAccount_StableToken(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "tienda")
	tableID := seedTbl(t, db, tenantID, "Mesa 1", "")
	pA := seedDirectProduct(t, db, tenantID, branchID, "Gaseosa", 2500, 10)
	pC := seedDirectProduct(t, db, tenantID, branchID, "Jugo", 4000, 10)
	r := mountPublicTableOrder(db)

	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{{"product_id": pA, "name": "Gaseosa", "quantity": 2, "price": 2500}},
	})
	first := openOrderForLabel(t, db, tenantID, "Mesa 1")

	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{{"product_id": pC, "name": "Jugo", "quantity": 1, "price": 4000}},
	})
	second := openOrderForLabel(t, db, tenantID, "Mesa 1")

	assert.Equal(t, first.ID, second.ID, "el 2º pedido reusa la MISMA cuenta")
	assert.Equal(t, first.SessionToken, second.SessionToken, "el token no se regenera")
	assert.Len(t, second.Items, 2)
	assert.Equal(t, 9000.0, second.Total) // 5000 + 4000
	assert.Equal(t, second.Total, ticketLineSum(second))

	var n int64
	db.Model(&models.OrderTicket{}).Where("tenant_id = ? AND label = ?", tenantID, "Mesa 1").Count(&n)
	assert.Equal(t, int64(1), n, "no debe crear una 2ª cuenta")
}

// ACC-MERGE-03 / BUG-PRICE-DRIFT-04 (corregido) — re-pedir el mismo producto
// funde en una fila y el Total = suma de líneas (sin drift), aun con precio distinto.
func TestPublicTableOrder_MergeSameProduct_NoDrift(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "tienda")
	tableID := seedTbl(t, db, tenantID, "Mesa 1", "")
	pA := seedDirectProduct(t, db, tenantID, branchID, "Gaseosa", 2500, 10)
	r := mountPublicTableOrder(db)

	// Mismo precio: qty se suma, una sola fila.
	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{{"product_id": pA, "name": "Gaseosa", "quantity": 2, "price": 2500}},
	})
	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{{"product_id": pA, "name": "Gaseosa", "quantity": 3, "price": 2500}},
	})
	o := openOrderForLabel(t, db, tenantID, "Mesa 1")
	assert.Len(t, o.Items, 1, "mismo producto = una sola fila")
	assert.Equal(t, 5, o.Items[0].Quantity)
	assert.Equal(t, 12500.0, o.Total)
	assert.Equal(t, o.Total, ticketLineSum(o), "Total siempre == suma de líneas")

	// Precio distinto: el Total NUNCA debe desincronizarse de la suma de líneas.
	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{{"product_id": pA, "name": "Gaseosa", "quantity": 1, "price": 9999}},
	})
	o2 := openOrderForLabel(t, db, tenantID, "Mesa 1")
	assert.Len(t, o2.Items, 1)
	assert.Equal(t, o2.Total, ticketLineSum(o2), "sin drift: Total == suma de líneas")
}

// BUG-DUP-NEWITEM-05 (corregido) — un producto NUEVO repetido dos veces en el
// MISMO request se funde en una fila con la cantidad sumada.
func TestPublicTableOrder_DuplicateNewItemInRequest_MergesToOneRow(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "tienda")
	tableID := seedTbl(t, db, tenantID, "Mesa 1", "")
	pD := seedDirectProduct(t, db, tenantID, branchID, "Arepa", 3000, 10)
	r := mountPublicTableOrder(db)

	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{
			{"product_id": pD, "name": "Arepa", "quantity": 1, "price": 3000},
			{"product_id": pD, "name": "Arepa", "quantity": 1, "price": 3000},
		},
	})
	o := openOrderForLabel(t, db, tenantID, "Mesa 1")
	assert.Len(t, o.Items, 1, "el producto repetido en el request no debe duplicar fila")
	assert.Equal(t, 2, o.Items[0].Quantity)
	assert.Equal(t, 6000.0, o.Total)
	assert.Equal(t, o.Total, ticketLineSum(o))
}

// SEC-TENANT-23 — aislamiento: mesa de otro tenant / slug inexistente / inactiva / body inválido.
func TestPublicTableOrder_Security(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantA, _ := seedTenantSlug(t, db, "brasas")
	tenantB, _ := seedTenantSlug(t, db, "donpepe")
	tableA := seedTbl(t, db, tenantA, "Mesa 1", "")
	tableB := seedTbl(t, db, tenantB, "Mesa 9", "")
	// Mesa inactiva de A. GORM omite el bool false (default:true), así que la
	// desactivamos explícitamente en BD tras crearla.
	inactive := uuid.NewString()
	require.NoError(t, db.Create(&models.Table{
		BaseModel: models.BaseModel{ID: inactive}, TenantID: tenantA, Label: "Mesa X", IsActive: false,
	}).Error)
	require.NoError(t, db.Model(&models.Table{}).Where("id = ?", inactive).
		Update("is_active", false).Error)
	r := mountPublicTableOrder(db)
	okItems := map[string]any{"items": []map[string]any{{"product_id": uuid.NewString(), "name": "X", "quantity": 1, "price": 1000}}}

	// (a) mesa de B vía slug de A → 404.
	assert.Equal(t, http.StatusNotFound, postTableOrder(t, r, "brasas", tableB, okItems).Code)
	// (b) slug inexistente → 404.
	assert.Equal(t, http.StatusNotFound, postTableOrder(t, r, "no-existe", tableA, okItems).Code)
	// (c) mesa inactiva → 404.
	assert.Equal(t, http.StatusNotFound, postTableOrder(t, r, "brasas", inactive, okItems).Code)
	// (d) body inválido (items vacío) → 400.
	assert.Equal(t, http.StatusBadRequest,
		postTableOrder(t, r, "brasas", tableA, map[string]any{"items": []map[string]any{}}).Code)

	// Nada se creó.
	var n int64
	db.Model(&models.OrderTicket{}).Count(&n)
	assert.Equal(t, int64(0), n)
}

// E2E CLOSE-SALE-10 — abrir por QR (producto directo) → cobrar (close) → stock
// baja exacto, Sale creada, ticket cobrado, 1 movimiento de venta. La mesa
// queda libre (ya no hay cuenta abierta).
func TestPublicTableOrder_E2E_OpenThenClose_InventoryExact(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "tienda")
	tableID := seedTbl(t, db, tenantID, "Mesa 1", "")
	p1 := seedDirectProduct(t, db, tenantID, branchID, "Cerveza", 4000, 30)
	r := mountPublicTableOrder(db)

	// Cliente pide por QR: 5 cervezas.
	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{{"product_id": p1, "name": "Cerveza", "quantity": 5, "price": 4000}},
	})
	o := openOrderForLabel(t, db, tenantID, "Mesa 1")
	// Aún ocupada, stock intacto.
	var p1row models.Product
	db.First(&p1row, "id = ?", p1)
	assert.Equal(t, 30, p1row.Stock, "no baja stock hasta cobrar")

	// Mesero cobra (close).
	rc := mountCloseOrderHandler(db, tenantID)
	w := doJSON(t, rc, http.MethodPost, "/orders/"+o.ID+"/close", map[string]any{"payment_method": "efectivo"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	db.First(&p1row, "id = ?", p1)
	assert.Equal(t, 25, p1row.Stock, "stock baja exactamente la cantidad acumulada (30-5)")

	var saleN, movN int64
	db.Model(&models.Sale{}).Where("tenant_id = ?", tenantID).Count(&saleN)
	db.Model(&models.InventoryMovement{}).Where("movement_type = ?", models.MovementSale).Count(&movN)
	assert.Equal(t, int64(1), saleN, "exactamente 1 venta")
	assert.Equal(t, int64(1), movN, "exactamente 1 movimiento de venta")

	var closed models.OrderTicket
	db.First(&closed, "id = ?", o.ID)
	assert.Equal(t, models.OrderStatusCobrado, closed.Status)

	// Mesa libre: ya no hay cuenta abierta para ese label.
	var open int64
	db.Model(&models.OrderTicket{}).
		Where("tenant_id = ? AND label = ? AND status IN ?", tenantID, "Mesa 1",
			[]string{"nuevo", "preparando", "listo"}).Count(&open)
	assert.Equal(t, int64(0), open, "la mesa queda libre al cobrar")
}

// BUG-CANCEL-INFLATE-17 (corregido) — cancelar una cuenta ABIERTA (nunca cobrada)
// NO debe inflar el inventario (el tab nunca descontó stock).
func TestPublicTableOrder_CancelOpenTab_DoesNotInflateStock(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "tienda")
	tableID := seedTbl(t, db, tenantID, "Mesa 1", "")
	p1 := seedDirectProduct(t, db, tenantID, branchID, "Cerveza", 4000, 30)
	r := mountPublicTableOrder(db)

	postTableOrder(t, r, "tienda", tableID, map[string]any{
		"items": []map[string]any{{"product_id": p1, "name": "Cerveza", "quantity": 2, "price": 4000}},
	})
	o := openOrderForLabel(t, db, tenantID, "Mesa 1")

	// Cancelar la cuenta abierta (PATCH status=cancelado).
	gin.SetMode(gin.TestMode)
	rc := gin.New()
	rc.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() })
	rc.PATCH("/orders/:uuid", handlers.UpdateOrderStatus(db))
	w := doJSON(t, rc, http.MethodPatch, "/orders/"+o.ID, map[string]any{"status": "cancelado"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var p1row models.Product
	db.First(&p1row, "id = ?", p1)
	assert.Equal(t, 30, p1row.Stock, "cancelar una cuenta abierta NO infla el stock")
	var cancelMovs int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementOrderCancel).Count(&cancelMovs)
	assert.Equal(t, int64(0), cancelMovs, "sin movimiento de cancelación fantasma")
}

// seedRecipeProduct crea un plato (producto receta) con 1 insumo.
func seedRecipeProduct(t *testing.T, db *gorm.DB, tenantID, branchID string,
	name string, ingStock, perUnit float64) (productID, ingredientID string) {
	t.Helper()
	productID = uuid.NewString()
	recipeID := uuid.NewString()
	ingredientID = uuid.NewString()
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: ingredientID},
		TenantID:  tenantID, Name: "Insumo", Unit: models.UnitKg, Stock: ingStock,
	}).Error)
	pid := productID
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel: models.BaseModel{ID: recipeID}, TenantID: tenantID,
		ProductName: name, SalePrice: 12000, ProductID: &pid,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: recipeID, ProductName: "Insumo", Quantity: perUnit, IngredientID: &ingredientID},
		},
	}).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id, name, price, stock, is_available, is_recipe, recipe_id)
		 VALUES (?, ?, ?, ?, ?, ?, 12000, 0, 1, 1, ?)`,
		productID, time.Now(), time.Now(), tenantID, branchID, name, recipeID).Error)
	return productID, ingredientID
}

func mountCancelHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() })
	r.PATCH("/orders/:uuid", handlers.UpdateOrderStatus(db))
	return r
}

func makeTicket(t *testing.T, db *gorm.DB, tenantID, branchID, label string,
	status models.OrderStatus, productID string, qty int, price float64) string {
	t.Helper()
	id := uuid.NewString()
	bid := branchID
	require.NoError(t, db.Create(&models.OrderTicket{
		BaseModel: models.BaseModel{ID: id}, TenantID: tenantID, BranchID: &bid,
		Label: label, Status: status, Type: models.OrderTypeMesa, Total: price * float64(qty),
		Items: []models.OrderItem{{OrderUUID: id, ProductUUID: productID,
			ProductName: "x", Quantity: qty, UnitPrice: price}},
	}).Error)
	return id
}

// Spec 083 (fundador) — cancelar una HAMBURGUESA ya preparada ('listo') registra
// la merma de sus insumos (se consumieron al cocinar). No se restauran.
func TestCancelPreparedRecipe_RecordsMerma(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "rest")
	prod, ing := seedRecipeProduct(t, db, tenantID, branchID, "Hamburguesa", 10, 0.2)
	orderID := makeTicket(t, db, tenantID, branchID, "Mesa 1", models.OrderStatusListo, prod, 3, 12000)

	w := doJSON(t, mountCancelHandler(db, tenantID), http.MethodPatch,
		"/orders/"+orderID, map[string]any{"status": "cancelado"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var i models.Ingredient
	require.NoError(t, db.First(&i, "id = ?", ing).Error)
	assert.InDelta(t, 9.4, i.Stock, 1e-9, "merma: 10 - 3*0.2 = 9.4 (insumos consumidos)")
	var consumos int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).Count(&consumos)
	assert.Equal(t, int64(1), consumos, "registra el consumo (merma) del insumo")
}

// Cancelar una hamburguesa AÚN 'nuevo' (no cocinada) NO registra merma.
func TestCancelNewRecipe_NoMerma(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "rest")
	prod, ing := seedRecipeProduct(t, db, tenantID, branchID, "Hamburguesa", 10, 0.2)
	orderID := makeTicket(t, db, tenantID, branchID, "Mesa 1", models.OrderStatusNuevo, prod, 3, 12000)

	w := doJSON(t, mountCancelHandler(db, tenantID), http.MethodPatch,
		"/orders/"+orderID, map[string]any{"status": "cancelado"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var i models.Ingredient
	require.NoError(t, db.First(&i, "id = ?", ing).Error)
	assert.InDelta(t, 10.0, i.Stock, 1e-9, "plato no cocinado: sin merma")
}

// Cancelar una GASEOSA (producto directo) 'listo' NO afecta inventario (se revende).
func TestCancelPreparedDirect_NoMerma(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "rest")
	soda := seedDirectProduct(t, db, tenantID, branchID, "Gaseosa", 2500, 20)
	orderID := makeTicket(t, db, tenantID, branchID, "Mesa 1", models.OrderStatusListo, soda, 2, 2500)

	w := doJSON(t, mountCancelHandler(db, tenantID), http.MethodPatch,
		"/orders/"+orderID, map[string]any{"status": "cancelado"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", soda).Error)
	assert.Equal(t, 20, p.Stock, "gaseosa cancelada se revende: stock intacto")
}

// council BUG-DUP-ACCOUNT-RACE — el índice único parcial impide DOS cuentas
// ABIERTAS para la misma (tenant,label). Una segunda inserción de cuenta abierta
// con el mismo label debe fallar a nivel BD.
func TestOpenTableAccount_UniqueIndex_RejectsDuplicate(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "rest")
	makeTicket(t, db, tenantID, branchID, "Mesa 1", models.OrderStatusNuevo,
		seedDirectProduct(t, db, tenantID, branchID, "Gaseosa", 2500, 10), 1, 2500)

	// Segunda cuenta ABIERTA para 'Mesa 1' → viola el índice único parcial.
	dup := models.OrderTicket{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		TenantID:  tenantID, Label: "Mesa 1",
		Status: models.OrderStatusNuevo, Type: models.OrderTypeMesa,
	}
	err := db.Create(&dup).Error
	require.Error(t, err, "no debe permitir 2 cuentas abiertas para la misma mesa")
	assert.Contains(t, strings.ToUpper(err.Error()), "UNIQUE", "el error es de índice único: %v", err)

	// Pero una cuenta CERRADA con el mismo label sí se permite (no choca el parcial).
	closed := models.OrderTicket{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		TenantID:  tenantID, Label: "Mesa 1",
		Status: models.OrderStatusCobrado, Type: models.OrderTypeMesa,
	}
	assert.NoError(t, db.Create(&closed).Error, "una cuenta cobrada no choca con el índice parcial")
}

// council BUG-STOCK0-NOKARDEX — cobrar un producto directo con stock=0 SÍ
// descuenta (queda negativo) y registra el movimiento de venta (trazabilidad).
func TestCloseOrder_StockZero_RecordsKardexAndGoesNegative(t *testing.T) {
	db := setupPublicTableOrderDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "rest")
	p0 := seedDirectProduct(t, db, tenantID, branchID, "Cerveza", 4000, 0) // stock 0
	orderID := makeTicket(t, db, tenantID, branchID, "Mesa 1", models.OrderStatusListo, p0, 2, 4000)

	w := doJSON(t, mountCloseOrderHandler(db, tenantID), http.MethodPost,
		"/orders/"+orderID+"/close", map[string]any{"payment_method": "efectivo"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", p0).Error)
	assert.Equal(t, -2, p.Stock, "stock baja a negativo (vendido sin existencias)")
	var movs int64
	db.Model(&models.InventoryMovement{}).
		Where("product_id = ? AND movement_type = ?", p0, models.MovementSale).Count(&movs)
	assert.Equal(t, int64(1), movs, "toda venta queda trazada en kardex aunque el stock sea 0")
}

// council BUG-CLOSE-RACE — dos cierres CONCURRENTES del mismo pedido producen
// UNA sola venta y descuentan el inventario UNA sola vez (lock + update condicional).
func TestCloseOrder_Concurrent_OneSale(t *testing.T) {
	db := setupSharedMesaDB(t)
	tenantID, branchID := seedTenantSlug(t, db, "rest")
	p := seedDirectProduct(t, db, tenantID, branchID, "Cerveza", 4000, 30)
	orderID := makeTicket(t, db, tenantID, branchID, "Mesa 1", models.OrderStatusListo, p, 5, 4000)
	r := mountCloseOrderHandler(db, tenantID)

	body, _ := json.Marshal(map[string]any{"payment_method": "efectivo"})
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/orders/"+orderID+"/close", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	assert.Equal(t, 1, ok, "exactamente un cierre exitoso; codes=%v", codes)
	var sales int64
	db.Model(&models.Sale{}).Where("tenant_id = ?", tenantID).Count(&sales)
	assert.Equal(t, int64(1), sales, "una sola venta pese al doble cierre concurrente")
	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", p).Error)
	assert.Equal(t, 25, prod.Stock, "stock descontado una sola vez (30-5)")
}

// Nota: la carrera de DOS PRIMEROS pedidos concurrentes a una mesa vacía está
// cubierta por: (1) TestOpenTableAccount_UniqueIndex_RejectsDuplicate, que prueba
// que la BD impide 2 cuentas abiertas para la misma (tenant,label); y (2) el retry
// `isRetryableConflict` en el handler, que ante esa violación reintenta y ACUMULA
// en la cuenta del ganador. No se prueba con goroutines porque el driver sqlite
// de tests serializa/devuelve "database is locked" de forma no determinista; en
// Postgres (prod) el lock de fila + el índice único parcial lo resuelven.
