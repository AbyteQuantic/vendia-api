// Spec: specs/005-fixes-regresion-360/spec.md
package services

import (
	"bytes"
	"log"
	"testing"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSaleInventoryDB migrates the schema ApplyPostSale touches:
// Product, Recipe, RecipeIngredient, Ingredient and InventoryMovement.
func setupSaleInventoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{},
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Ingredient{},
		&models.InventoryMovement{},
	))
	return db
}

// FR-02 — a direct product line decrements its own stock and logs a
// single `sale` movement.
func TestApplyPostSale_DirectProduct_DecrementsStockAndLogsMovement(t *testing.T) {
	db := setupSaleInventoryDB(t)
	tenantID := "tenant-spd"
	productID := "a0000000-0000-4000-8000-000000000030"

	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500, Stock: 20,
		IsAvailable: true, IsRecipe: false,
	}).Error)

	svc := NewSaleInventoryService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, PostSaleParams{
			TenantID: tenantID,
			SaleUUID: "5a1e0000-0000-4000-8000-000000000030",
			Lines:    []SaleInventoryLine{{ProductID: productID, Quantity: 3}},
		})
	})
	require.NoError(t, err)

	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", productID).Error)
	assert.Equal(t, 17, product.Stock, "20 - 3 = 17")

	var saleMovs int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementSale).Count(&saleMovs)
	assert.Equal(t, int64(1), saleMovs, "one `sale` movement")
}

// FR-02 — a recipe line explodes into insumo consumption.
func TestApplyPostSale_RecipeProduct_ExplodesIngredients(t *testing.T) {
	db := setupSaleInventoryDB(t)
	tenantID := "tenant-spr"
	productID := "a0000000-0000-4000-8000-000000000031"
	recipeID := "b0000000-0000-4000-8000-000000000031"
	arrozID := "c0000000-0000-4000-8000-000000000031"

	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: arrozID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg, Stock: 5,
	}).Error)
	pid := productID
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Almuerzo",
		SalePrice:   10000,
		ProductID:   &pid,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: recipeID, ProductName: "Arroz", Quantity: 0.5, IngredientID: &arrozID},
		},
	}).Error)
	rid := recipeID
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Almuerzo", Price: 10000, Stock: 0,
		IsAvailable: true, IsRecipe: true, RecipeID: &rid,
	}).Error)

	svc := NewSaleInventoryService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, PostSaleParams{
			TenantID: tenantID,
			SaleUUID: "5a1e0000-0000-4000-8000-000000000031",
			Lines:    []SaleInventoryLine{{ProductID: productID, Quantity: 2}},
		})
	})
	require.NoError(t, err)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", arrozID).Error)
	assert.InDelta(t, 4.0, arroz.Stock, 1e-9, "5 - 2*0.5 = 4.0")
}

// FR-02 — a line with zero quantity is skipped (no movement, no
// decrement). Guards the `Quantity <= 0` branch.
func TestApplyPostSale_ZeroQuantity_IsSkipped(t *testing.T) {
	db := setupSaleInventoryDB(t)
	tenantID := "tenant-spz"
	productID := "a0000000-0000-4000-8000-000000000032"

	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500, Stock: 20,
		IsAvailable: true,
	}).Error)

	svc := NewSaleInventoryService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, PostSaleParams{
			TenantID: tenantID,
			SaleUUID: "5a1e0000-0000-4000-8000-000000000032",
			Lines:    []SaleInventoryLine{{ProductID: productID, Quantity: 0}},
		})
	})
	require.NoError(t, err)

	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", productID).Error)
	assert.Equal(t, 20, product.Stock, "zero-quantity line must not decrement")

	var movs int64
	db.Model(&models.InventoryMovement{}).Count(&movs)
	assert.Equal(t, int64(0), movs, "zero-quantity line logs no movement")
}

// FR-02 — a product that does not belong to the tenant is skipped
// silently; the sale is never aborted over an inventory miss. Also
// guards multi-tenant isolation (Constitución Art. III).
func TestApplyPostSale_ForeignTenantProduct_IsSkipped(t *testing.T) {
	db := setupSaleInventoryDB(t)
	ownerTenant := "tenant-owner"
	foreignTenant := "tenant-foreign"
	productID := "a0000000-0000-4000-8000-000000000033"

	// The product belongs to the FOREIGN tenant.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  foreignTenant, Name: "Gaseosa", Price: 2500, Stock: 20,
		IsAvailable: true,
	}).Error)

	svc := NewSaleInventoryService(db)
	// The owner tenant tries to apply inventory for it — must be a no-op.
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, PostSaleParams{
			TenantID: ownerTenant,
			SaleUUID: "5a1e0000-0000-4000-8000-000000000033",
			Lines:    []SaleInventoryLine{{ProductID: productID, Quantity: 5}},
		})
	})
	require.NoError(t, err, "an inventory miss must never abort the sale")

	// The foreign product's stock is untouched.
	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", productID).Error)
	assert.Equal(t, 20, product.Stock, "a foreign tenant must NEVER move another tenant's stock")
}

// FR-02 — re-applying the same sale UUID does not discount insumos
// twice (idempotency anchor — Constitución Art. II).
func TestApplyPostSale_Idempotent_NoDoubleRecipeDiscount(t *testing.T) {
	db := setupSaleInventoryDB(t)
	tenantID := "tenant-spi"
	productID := "a0000000-0000-4000-8000-000000000034"
	recipeID := "b0000000-0000-4000-8000-000000000034"
	arrozID := "c0000000-0000-4000-8000-000000000034"

	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: arrozID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg, Stock: 5,
	}).Error)
	pid := productID
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Almuerzo",
		SalePrice:   10000,
		ProductID:   &pid,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: recipeID, ProductName: "Arroz", Quantity: 0.5, IngredientID: &arrozID},
		},
	}).Error)
	rid := recipeID
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Almuerzo", Price: 10000, Stock: 0,
		IsAvailable: true, IsRecipe: true, RecipeID: &rid,
	}).Error)

	svc := NewSaleInventoryService(db)
	saleUUID := "5a1e0000-0000-4000-8000-000000000034"
	apply := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return svc.ApplyPostSale(tx, PostSaleParams{
				TenantID: tenantID,
				SaleUUID: saleUUID,
				Lines:    []SaleInventoryLine{{ProductID: productID, Quantity: 1}},
			})
		})
	}
	require.NoError(t, apply())
	require.NoError(t, apply()) // re-apply same sale

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", arrozID).Error)
	assert.InDelta(t, 4.5, arroz.Stock, 1e-9, "5 - 0.5 — discounted exactly once")
}

// Spec 077 — un producto GLOBAL (branch NULL: menú/servicio) se vende desde
// CUALQUIER sede y SÍ descuenta su stock. Antes el lookup filtraba branch_id=?
// sin incluir NULL → saltaba el producto global y NO descontaba (regresión).
func TestApplyPostSale_GlobalProduct_DecrementsFromAnySede(t *testing.T) {
	db := setupSaleInventoryDB(t)
	tenantID := "tenant-global"
	productID := "b0000000-0000-4000-8000-000000000099"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Almuerzo del día", Price: 14000, Stock: 10,
		IsAvailable: true, IsMenuItem: true, BranchID: nil, // GLOBAL
	}).Error)

	svc := NewSaleInventoryService(db)
	sedeA := "br-a-0000-0000-4000-8000-000000000001"
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, PostSaleParams{
			TenantID: tenantID, BranchID: &sedeA, // vende desde la sede A
			SaleUUID: "5a1e0000-0000-4000-8000-000000000099",
			Lines:    []SaleInventoryLine{{ProductID: productID, Quantity: 4}},
		})
	})
	require.NoError(t, err)

	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", productID).Error)
	assert.Equal(t, 6, product.Stock, "global vendido desde sede A: 10 - 4 = 6")
}

// M14 — a kardex write failure on the direct-product path must NOT abort
// the sale (Constitución Art. I: la venta nunca se pierde por un hiccup
// de kardex), but it must leave a trace in the logs so a real failure is
// diagnosable after the fact. Simulated by dropping inventory_movements
// out from under LogInventoryMovement so its tx.Create call errors.
func TestApplyPostSale_KardexWriteFails_LogsButDoesNotAbort(t *testing.T) {
	db := setupSaleInventoryDB(t)
	tenantID := "tenant-spk"
	productID := "a0000000-0000-4000-8000-000000000035"
	saleUUID := "5a1e0000-0000-4000-8000-000000000035"

	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500, Stock: 20,
		IsAvailable: true, IsRecipe: false,
	}).Error)

	// Drop the inventory_movements table so LogInventoryMovement's
	// tx.Create(&mov) errors out — simulates a real kardex hiccup
	// (constraint change / dropped connection) without touching the
	// service's control flow.
	require.NoError(t, db.Migrator().DropTable(&models.InventoryMovement{}))

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	}()

	svc := NewSaleInventoryService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, PostSaleParams{
			TenantID: tenantID,
			SaleUUID: saleUUID,
			Lines:    []SaleInventoryLine{{ProductID: productID, Quantity: 3}},
		})
	})
	require.NoError(t, err, "la venta NUNCA debe fallar por un hiccup de kardex")

	// The sale-level "never abort" contract holds...
	var product models.Product
	require.NoError(t, db.First(&product, "id = ?", productID).Error)
	assert.Equal(t, 17, product.Stock, "el stock SÍ se descuenta aunque el kardex falle")

	// ...but the failure must be traceable in the logs, with enough
	// context (tenant/sale/product) to diagnose it later.
	logged := logBuf.String()
	assert.Contains(t, logged, "[sale-inventory]")
	assert.Contains(t, logged, tenantID)
	assert.Contains(t, logged, saleUUID)
	assert.Contains(t, logged, productID)
}
