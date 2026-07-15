// Spec: specs/005-fixes-regresion-360/spec.md
package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupCloseOrderDB hand-crafts the schema CloseOrder touches end to
// end: tenants/branches/products/sales/sale_items carry Postgres-only
// defaults so they are created as raw DDL (mirroring the recipe-sale
// test); the Feature-001 tables (recipe / recipe_ingredient /
// ingredient / inventory_movement) and the order_ticket / order_item
// tables AutoMigrate cleanly from the structs.
func setupCloseOrderDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	stmts := []string{
		`CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '', phone TEXT DEFAULT '',
			owner_name TEXT DEFAULT '', created_at DATETIME
		)`,
		`CREATE TABLE branches (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL, address TEXT DEFAULT '',
			is_active INTEGER DEFAULT 1
		)`,
		`CREATE TABLE products (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			branch_id TEXT,
			name TEXT NOT NULL, price REAL NOT NULL DEFAULT 0,
			stock INTEGER NOT NULL DEFAULT 0,
			is_available INTEGER DEFAULT 1,
			requires_container INTEGER DEFAULT 0,
			container_price INTEGER DEFAULT 0,
			purchase_price REAL DEFAULT 0,
			barcode TEXT DEFAULT '',
			image_url TEXT DEFAULT '',
			ingestion_method TEXT DEFAULT 'manual',
			expiry_date DATETIME,
			is_recipe INTEGER DEFAULT 0,
			recipe_id TEXT
		)`,
		`CREATE TABLE sales (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			branch_id TEXT, created_by TEXT,
			employee_uuid TEXT, employee_name TEXT DEFAULT '',
			receipt_number INTEGER DEFAULT 0,
			total REAL NOT NULL DEFAULT 0,
			tax_amount REAL DEFAULT 0,
			tip_amount REAL DEFAULT 0,
			payment_method TEXT NOT NULL,
			customer_id TEXT, customer_name_snapshot TEXT DEFAULT '',
			customer_phone_snapshot TEXT DEFAULT '',
			is_credit INTEGER DEFAULT 0,
			credit_account_id TEXT,
			payment_status TEXT DEFAULT 'COMPLETED',
			dynamic_qr_payload TEXT,
			source TEXT NOT NULL DEFAULT 'POS',
			receipt_image_url TEXT DEFAULT '',
			price_tier TEXT NOT NULL DEFAULT 'retail',
			cash_shift_uuid TEXT,
			-- Spec F031: link back to the converted quote.
			quote_id TEXT,
			cost_amount REAL DEFAULT 0,
			event_registration_id TEXT
		)`,
		`CREATE TABLE sale_items (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, sale_id TEXT NOT NULL,
			product_id TEXT, name TEXT NOT NULL,
			price REAL NOT NULL DEFAULT 0,
			quantity INTEGER NOT NULL,
			subtotal REAL NOT NULL DEFAULT 0,
			is_container_charge INTEGER DEFAULT 0,
			is_service INTEGER DEFAULT 0,
			custom_description TEXT DEFAULT '',
			custom_unit_price REAL DEFAULT 0,
			employee_uuid TEXT, employee_name TEXT DEFAULT '', pay_basis TEXT DEFAULT 'none', commission_pct REAL, commission_amount REAL DEFAULT 0
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	require.NoError(t, db.AutoMigrate(
		&models.OrderTicket{},
		&models.OrderItem{},
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Ingredient{},
		&models.InventoryMovement{},
	))
	return db
}

func mountCloseOrderHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/orders/:uuid/close", handlers.CloseOrder(db))
	return r
}

// AC-02 — closing a KDS order that contains a product-receta must
// discount the insumos and log the recipe_consumption movements, just
// like a sale through CreateSale. The bug: CloseOrder did a bare
// tx.Create(&sale) with no inventory side effects.
func TestCloseOrder_RecipeProduct_DiscountsIngredients(t *testing.T) {
	db := setupCloseOrderDB(t)
	tenantID := "tenant-close-rcp"
	branchID := "e0000000-0000-4000-8000-000000000020"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	productID := "a0000000-0000-4000-8000-000000000020"
	recipeID := "b0000000-0000-4000-8000-000000000020"
	arrozID := "c0000000-0000-4000-8000-000000000020"
	polloID := "d0000000-0000-4000-8000-000000000020"

	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: arrozID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg, Stock: 3,
	}).Error)
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: polloID},
		TenantID:  tenantID, Name: "Pollo", Unit: models.UnitKg, Stock: 2,
	}).Error)
	pid := productID
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Almuerzo corriente",
		SalePrice:   12000,
		ProductID:   &pid,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: recipeID, ProductName: "Arroz", Quantity: 0.15, IngredientID: &arrozID},
			{RecipeUUID: recipeID, ProductName: "Pollo", Quantity: 0.2, IngredientID: &polloID},
		},
	}).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe, recipe_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 1, 1, ?)`,
		productID, time.Now(), time.Now(), tenantID, branchID,
		"Almuerzo corriente", 12000, recipeID).Error)

	// A KDS order for 2 "Almuerzo corriente".
	orderID := "f0000000-0000-4000-8000-000000000020"
	bid := branchID
	require.NoError(t, db.Create(&models.OrderTicket{
		BaseModel: models.BaseModel{ID: orderID},
		TenantID:  tenantID, BranchID: &bid, Label: "Mesa 1",
		Status: models.OrderStatusListo, Type: models.OrderTypeMesa, Total: 24000,
		Items: []models.OrderItem{
			{OrderUUID: orderID, ProductUUID: productID, ProductName: "Almuerzo corriente",
				Quantity: 2, UnitPrice: 12000},
		},
	}).Error)

	r := mountCloseOrderHandler(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/orders/"+orderID+"/close", map[string]any{
		"payment_method": "cash",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Insumos discounted: arroz 3 - 2*0.15 = 2.70, pollo 2 - 2*0.20 = 1.60.
	var arroz, pollo models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", arrozID).Error)
	require.NoError(t, db.First(&pollo, "id = ?", polloID).Error)
	assert.InDelta(t, 2.70, arroz.Stock, 1e-9, "arroz must be discounted on order close")
	assert.InDelta(t, 1.60, pollo.Stock, 1e-9, "pollo must be discounted on order close")

	// Two recipe_consumption movements.
	var recipeMovs int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).Count(&recipeMovs)
	assert.Equal(t, int64(2), recipeMovs, "two recipe_consumption movements expected")

	// The sale row was created.
	var saleCount int64
	db.Model(&models.Sale{}).Where("tenant_id = ?", tenantID).Count(&saleCount)
	assert.Equal(t, int64(1), saleCount, "the sale ledger row must exist")

	// Order is marked cobrado.
	var order models.OrderTicket
	require.NoError(t, db.First(&order, "id = ?", orderID).Error)
	assert.Equal(t, models.OrderStatusCobrado, order.Status)
}

// AC-02 — closing a KDS order with a DIRECT product decrements that
// product's own stock and logs a `sale` movement.
func TestCloseOrder_DirectProduct_DecrementsStock(t *testing.T) {
	db := setupCloseOrderDB(t)
	tenantID := "tenant-close-dir"
	branchID := "e0000000-0000-4000-8000-000000000021"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	directID := "a0000000-0000-4000-8000-000000000021"
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe)
		VALUES (?, ?, ?, ?, ?, 'Cerveza', 4000, 30, 1, 0)`,
		directID, time.Now(), time.Now(), tenantID, branchID).Error)

	orderID := "f0000000-0000-4000-8000-000000000021"
	bid := branchID
	require.NoError(t, db.Create(&models.OrderTicket{
		BaseModel: models.BaseModel{ID: orderID},
		TenantID:  tenantID, BranchID: &bid, Label: "Mesa 2",
		Status: models.OrderStatusListo, Type: models.OrderTypeMesa, Total: 16000,
		Items: []models.OrderItem{
			{OrderUUID: orderID, ProductUUID: directID, ProductName: "Cerveza",
				Quantity: 4, UnitPrice: 4000},
		},
	}).Error)

	r := mountCloseOrderHandler(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/orders/"+orderID+"/close", map[string]any{
		"payment_method": "cash",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Direct product stock drops 30 → 26.
	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", directID).Error)
	assert.Equal(t, 26, prod.Stock, "direct product stock must decrement on order close")

	// One `sale` movement, zero recipe_consumption.
	var saleMovs, recipeMovs int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementSale).Count(&saleMovs)
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).Count(&recipeMovs)
	assert.Equal(t, int64(1), saleMovs, "a `sale` movement must be logged on order close")
	assert.Equal(t, int64(0), recipeMovs, "a direct product produces no recipe_consumption")
}

// AC-02 / idempotency — re-closing an already-cobrado order is rejected
// and the inventory is NOT discounted a second time.
func TestCloseOrder_AlreadyClosed_NoDoubleDiscount(t *testing.T) {
	db := setupCloseOrderDB(t)
	tenantID := "tenant-close-idem"
	branchID := "e0000000-0000-4000-8000-000000000022"

	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	directID := "a0000000-0000-4000-8000-000000000022"
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe)
		VALUES (?, ?, ?, ?, ?, 'Cerveza', 4000, 30, 1, 0)`,
		directID, time.Now(), time.Now(), tenantID, branchID).Error)

	orderID := "f0000000-0000-4000-8000-000000000022"
	bid := branchID
	require.NoError(t, db.Create(&models.OrderTicket{
		BaseModel: models.BaseModel{ID: orderID},
		TenantID:  tenantID, BranchID: &bid, Label: "Mesa 3",
		Status: models.OrderStatusListo, Type: models.OrderTypeMesa, Total: 8000,
		Items: []models.OrderItem{
			{OrderUUID: orderID, ProductUUID: directID, ProductName: "Cerveza",
				Quantity: 2, UnitPrice: 4000},
		},
	}).Error)

	r := mountCloseOrderHandler(db, tenantID)
	body := map[string]any{"payment_method": "cash"}

	w1 := doJSON(t, r, http.MethodPost, "/orders/"+orderID+"/close", body)
	require.Equal(t, http.StatusOK, w1.Code, w1.Body.String())
	// Second close — order already cobrado, rejected.
	w2 := doJSON(t, r, http.MethodPost, "/orders/"+orderID+"/close", body)
	assert.Equal(t, http.StatusBadRequest, w2.Code, "re-closing must be rejected")

	// Stock dropped exactly once: 30 → 28.
	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", directID).Error)
	assert.Equal(t, 28, prod.Stock, "stock must drop only once — no double discount")
}
