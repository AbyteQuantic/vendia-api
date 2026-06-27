// Spec: specs/001-insumos-recetas/spec.md
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

// setupSaleRecipeDB hand-crafts the sqlite schema CreateSale touches,
// extended with the Feature-001 columns and tables. The products/sales
// schema mirrors branch_isolation_test (production models carry
// Postgres-only defaults) but adds is_recipe / recipe_id, and the
// recipe / recipe_ingredient / ingredient / inventory_movement tables
// are AutoMigrated from the structs (they have no Postgres-only DDL).
func setupSaleRecipeDB(t *testing.T) *gorm.DB {
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
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Ingredient{},
		&models.InventoryMovement{},
	))
	return db
}

func mountSaleRecipeHandler(db *gorm.DB, tenantID, branchID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		if branchID != "" {
			c.Set(middleware.BranchIDKey, branchID)
		}
		c.Next()
	})
	r.POST("/api/v1/sales", handlers.CreateSale(db, nil))
	return r
}

func seedRecipeProductForSale(t *testing.T, db *gorm.DB, tenantID, branchID string) recipeSaleFixture {
	t.Helper()
	f := recipeSaleFixture{
		productID: "a0000000-0000-4000-8000-000000000010",
		recipeID:  "b0000000-0000-4000-8000-000000000010",
		arrozID:   "c0000000-0000-4000-8000-000000000010",
		polloID:   "d0000000-0000-4000-8000-000000000010",
	}
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: f.arrozID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg, Stock: 3,
	}).Error)
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: f.polloID},
		TenantID:  tenantID, Name: "Pollo", Unit: models.UnitKg, Stock: 2,
	}).Error)
	rid := f.recipeID
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: f.recipeID},
		TenantID:    tenantID,
		ProductName: "Almuerzo corriente",
		SalePrice:   12000,
		ProductID:   &f.productID,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: rid, ProductName: "Arroz", Quantity: 0.15, IngredientID: &f.arrozID},
			{RecipeUUID: rid, ProductName: "Pollo", Quantity: 0.2, IngredientID: &f.polloID},
		},
	}).Error)
	// The vendible product-receta. Stock is left 0: a recipe product
	// has no own stock (D1) — selling it must NOT depend on it.
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe, recipe_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 1, 1, ?)`,
		f.productID, time.Now(), time.Now(), tenantID, branchID,
		"Almuerzo corriente", 12000, rid).Error)
	return f
}

type recipeSaleFixture struct {
	productID, recipeID, arrozID, polloID string
}

// AC-04 — selling 2 "Almuerzo corriente" via CreateSale discounts the
// insumos (arroz -0.30, pollo -0.40) and logs two recipe_consumption
// movements. This is the end-to-end wiring of ExplodeRecipe into the
// sale path.
func TestCreateSale_RecipeProduct_DiscountsIngredients(t *testing.T) {
	db := setupSaleRecipeDB(t)
	tenantID := "tenant-rcp"
	branchID := "e0000000-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	f := seedRecipeProductForSale(t, db, tenantID, branchID)
	r := mountSaleRecipeHandler(db, tenantID, branchID)

	w := doJSON(t, r, http.MethodPost, "/api/v1/sales", map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": f.productID, "quantity": 2},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var arroz, pollo models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	require.NoError(t, db.First(&pollo, "id = ?", f.polloID).Error)
	assert.InDelta(t, 2.70, arroz.Stock, 1e-9, "arroz: 3 - 2*0.15 = 2.70")
	assert.InDelta(t, 1.60, pollo.Stock, 1e-9, "pollo: 2 - 2*0.20 = 1.60")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).
		Count(&movCount)
	assert.Equal(t, int64(2), movCount, "two recipe_consumption movements expected")
}

// AC-06 (regression, SAGRADO) — selling a DIRECT product (no recipe)
// behaves exactly as before: its own stock decrements, a `sale`
// movement is logged, and NOT a single insumo or recipe_consumption
// movement is touched.
func TestCreateSale_DirectProduct_BehaviourUnchanged(t *testing.T) {
	db := setupSaleRecipeDB(t)
	tenantID := "tenant-dir"
	branchID := "f0000000-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	// An insumo exists for the tenant — the direct sale must NOT touch it.
	insumoID := "c9999999-0000-4000-8000-000000000010"
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: insumoID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg, Stock: 5,
	}).Error)

	directID := "a9999999-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe)
		VALUES (?, ?, ?, ?, ?, 'Gaseosa', 2500, 50, 1, 0)`,
		directID, time.Now(), time.Now(), tenantID, branchID).Error)

	r := mountSaleRecipeHandler(db, tenantID, branchID)
	w := doJSON(t, r, http.MethodPost, "/api/v1/sales", map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": directID, "quantity": 3},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// Direct product stock drops 50 → 47 — the legacy behaviour.
	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", directID).Error)
	assert.Equal(t, 47, prod.Stock, "direct product stock must decrement as before")

	// The insumo is completely untouched.
	var insumo models.Ingredient
	require.NoError(t, db.First(&insumo, "id = ?", insumoID).Error)
	assert.Equal(t, float64(5), insumo.Stock, "a direct sale must NEVER touch an insumo (AC-06)")

	// Exactly one `sale` movement, zero `recipe_consumption`.
	var saleMovs, recipeMovs int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementSale).Count(&saleMovs)
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).Count(&recipeMovs)
	assert.Equal(t, int64(1), saleMovs, "the legacy `sale` movement must still be logged")
	assert.Equal(t, int64(0), recipeMovs, "a direct sale must produce zero recipe_consumption movements")
}

// AC idempotency — re-POSTing the SAME sale UUID to CreateSale is
// rejected by the UNIQUE constraint on sales.id (the duplicate sale is
// not re-created). The contract that matters: the insumos are NOT
// discounted twice. The CreateSale transaction aborts on the duplicate,
// so the second explosion never runs. (The tolerant offline-re-sync
// path is /sync/batch — covered by the recipe_service idempotency
// test and the syncSale wiring.)
func TestCreateSale_RecipeProduct_DuplicateUUIDDoesNotDoubleDiscount(t *testing.T) {
	db := setupSaleRecipeDB(t)
	tenantID := "tenant-idem"
	branchID := "e1111111-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	f := seedRecipeProductForSale(t, db, tenantID, branchID)
	r := mountSaleRecipeHandler(db, tenantID, branchID)

	saleID := "9a1e0000-0000-4000-8000-000000000010"
	payload := map[string]any{
		"id":             saleID,
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": f.productID, "quantity": 1},
		},
	}
	// First sale — succeeds and discounts.
	w1 := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())
	// Re-POST the same UUID — rejected, the sale is not duplicated.
	w2 := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	assert.NotEqual(t, http.StatusCreated, w2.Code,
		"a duplicate sale UUID must be rejected, not re-created")

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 2.85, arroz.Stock, 1e-9,
		"arroz must drop only once (3 - 0.15) — no double discount")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).
		Count(&movCount)
	assert.Equal(t, int64(2), movCount, "no duplicate recipe_consumption movements")
}
