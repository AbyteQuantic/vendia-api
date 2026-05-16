// Spec: specs/001-insumos-recetas/spec.md
package services_test

import (
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupRecipeDB migrates the schema RecipeService.ExplodeRecipe touches:
// Product, Recipe, RecipeIngredient, Ingredient and InventoryMovement.
func setupRecipeDB(t *testing.T) *gorm.DB {
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

func strptr(s string) *string { return &s }

// seedRecipeProduct wires a product-receta: a Product flagged IsRecipe
// linked to a Recipe whose ingredients point at Ingredient rows.
type recipeFixture struct {
	tenantID    string
	productID   string
	recipeID    string
	arrozID     string
	polloID     string
}

func seedAlmuerzoCorriente(t *testing.T, db *gorm.DB) recipeFixture {
	t.Helper()
	f := recipeFixture{
		tenantID:  "tenant-a",
		productID: "p0000000-0000-4000-8000-000000000001",
		recipeID:  "r0000000-0000-4000-8000-000000000001",
		arrozID:   "10000000-0000-4000-8000-000000000001",
		polloID:   "20000000-0000-4000-8000-000000000001",
	}
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: f.arrozID},
		TenantID:  f.tenantID, Name: "Arroz", Unit: models.UnitKg,
		Stock: 3, MinStock: 1, UnitCost: 2900,
	}).Error)
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: f.polloID},
		TenantID:  f.tenantID, Name: "Pollo", Unit: models.UnitKg,
		Stock: 2, MinStock: 1, UnitCost: 12000,
	}).Error)
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: f.recipeID},
		TenantID:    f.tenantID,
		ProductName: "Almuerzo corriente",
		SalePrice:   12000,
		ProductID:   strptr(f.productID),
		Ingredients: []models.RecipeIngredient{
			{
				RecipeUUID: f.recipeID, ProductName: "Arroz",
				Quantity: 0.15, UnitCost: 2900, IngredientID: strptr(f.arrozID),
			},
			{
				RecipeUUID: f.recipeID, ProductName: "Pollo",
				Quantity: 0.2, UnitCost: 12000, IngredientID: strptr(f.polloID),
			},
		},
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: f.productID},
		TenantID:  f.tenantID, Name: "Almuerzo corriente",
		Price: 12000, IsRecipe: true, RecipeID: strptr(f.recipeID),
	}).Error)
	return f
}

// AC-04 — selling 2 "Almuerzo corriente" discounts arroz 0.30 kg,
// pollo 0.40 kg and writes one recipe_consumption movement per insumo.
func TestExplodeRecipe_DiscountsIngredientsAndLogsMovements(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db)
	svc := services.NewRecipeService(db)

	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID:  f.tenantID,
			SaleUUID:  "5a1e0000-0000-4000-8000-000000000001",
			ProductID: f.productID,
			Quantity:  2,
		})
	})
	require.NoError(t, err)

	var arroz, pollo models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	require.NoError(t, db.First(&pollo, "id = ?", f.polloID).Error)
	assert.InDelta(t, 2.70, arroz.Stock, 1e-9, "arroz 3 - 2*0.15 = 2.70")
	assert.InDelta(t, 1.60, pollo.Stock, 1e-9, "pollo 2 - 2*0.20 = 1.60")

	var movements []models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementRecipeConsumption).
		Find(&movements).Error)
	assert.Len(t, movements, 2, "one recipe_consumption movement per insumo")
}

// AC-04 / Art. II — re-exploding the SAME sale UUID is idempotent:
// ingredients are NOT discounted twice.
func TestExplodeRecipe_IdempotentBySaleUUID(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db)
	svc := services.NewRecipeService(db)

	explode := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return svc.ExplodeRecipe(tx, services.ExplodeParams{
				TenantID:  f.tenantID,
				SaleUUID:  "5a1e0000-0000-4000-8000-000000000002",
				ProductID: f.productID,
				Quantity:  1,
			})
		})
	}
	require.NoError(t, explode())
	require.NoError(t, explode()) // re-sync of the same sale

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 2.85, arroz.Stock, 1e-9,
		"arroz must drop only ONCE (3 - 0.15) even after a re-sync")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).
		Count(&movCount)
	assert.Equal(t, int64(2), movCount,
		"re-exploding the same sale must not append duplicate movements")
}

// AC-07 — when an insumo lacks enough stock the explosion still
// proceeds: the venta is never lost (D3). The insumo goes negative
// and that is visible in the kardex.
func TestExplodeRecipe_AllowsNegativeStockWhenInsufficient(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db)
	svc := services.NewRecipeService(db)

	// Sell 20 almuerzos — needs 3.0 kg arroz, only 3 available, and
	// 4.0 kg pollo, only 2 available → pollo goes negative.
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID:  f.tenantID,
			SaleUUID:  "5a1e0000-0000-4000-8000-000000000003",
			ProductID: f.productID,
			Quantity:  20,
		})
	})
	require.NoError(t, err, "the explosion must NOT fail on insufficient stock — venta nunca se pierde")

	var pollo models.Ingredient
	require.NoError(t, db.First(&pollo, "id = ?", f.polloID).Error)
	assert.InDelta(t, -2.0, pollo.Stock, 1e-9,
		"pollo 2 - 20*0.2 = -2.0 — negative is allowed and visible")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).
		Count(&movCount)
	assert.Equal(t, int64(2), movCount, "the consumption is still logged for the kardex")
}

// A direct product (no IsRecipe flag) is a no-op for ExplodeRecipe:
// the service must silently skip it so CreateSale can call it on every
// item without caring whether it is a recipe.
func TestExplodeRecipe_NoOpForDirectProduct(t *testing.T) {
	db := setupRecipeDB(t)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "p0000000-0000-4000-8000-000000000099"},
		TenantID:  "tenant-a", Name: "Gaseosa", Price: 2500, IsRecipe: false,
	}).Error)
	svc := services.NewRecipeService(db)

	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID:  "tenant-a",
			SaleUUID:  "5a1e0000-0000-4000-8000-000000000004",
			ProductID: "p0000000-0000-4000-8000-000000000099",
			Quantity:  3,
		})
	})
	require.NoError(t, err)

	var movCount int64
	db.Model(&models.InventoryMovement{}).Count(&movCount)
	assert.Equal(t, int64(0), movCount, "a direct product produces zero movements")
}

// resolveRecipe fallback — when Product.RecipeID is nil the service
// finds the recipe via Recipe.ProductID pointing back at the product.
func TestExplodeRecipe_ResolvesViaRecipeProductID(t *testing.T) {
	db := setupRecipeDB(t)
	tenantID := "tenant-a"
	productID := "p1000000-0000-4000-8000-000000000001"
	recipeID := "r1000000-0000-4000-8000-000000000001"
	arrozID := "11000000-0000-4000-8000-000000000001"

	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: arrozID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg, Stock: 3,
	}).Error)
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Almuerzo",
		SalePrice:   12000,
		ProductID:   strptr(productID), // recipe → product link
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: recipeID, ProductName: "Arroz", Quantity: 0.15, IngredientID: strptr(arrozID)},
		},
	}).Error)
	// Product has IsRecipe=true but RecipeID nil — resolution must
	// fall back to Recipe.ProductID.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Almuerzo", Price: 12000, IsRecipe: true,
	}).Error)

	svc := services.NewRecipeService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID: tenantID, SaleUUID: "5a1e0000-0000-4000-8000-0000000000a1",
			ProductID: productID, Quantity: 1,
		})
	})
	require.NoError(t, err)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", arrozID).Error)
	assert.InDelta(t, 2.85, arroz.Stock, 1e-9, "resolved via Recipe.ProductID and discounted")
}

// A recipe-flagged product with NO recipe at all is a safe no-op: the
// venta still goes through, nothing is exploded (D3).
func TestExplodeRecipe_RecipeFlaggedProductWithoutRecipe(t *testing.T) {
	db := setupRecipeDB(t)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "p2000000-0000-4000-8000-000000000001"},
		TenantID:  "tenant-a", Name: "Fantasma", Price: 1000, IsRecipe: true,
	}).Error)
	svc := services.NewRecipeService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID: "tenant-a", SaleUUID: "5a1e0000-0000-4000-8000-0000000000a2",
			ProductID: "p2000000-0000-4000-8000-000000000001", Quantity: 1,
		})
	})
	require.NoError(t, err)
	var movCount int64
	db.Model(&models.InventoryMovement{}).Count(&movCount)
	assert.Equal(t, int64(0), movCount)
}

// A legacy recipe line still pointing at a Product (IngredientID nil)
// is skipped — never guessed at (Art. IV).
func TestExplodeRecipe_SkipsLegacyLineWithoutIngredientID(t *testing.T) {
	db := setupRecipeDB(t)
	tenantID := "tenant-a"
	productID := "p3000000-0000-4000-8000-000000000001"
	recipeID := "r3000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Receta vieja",
		SalePrice:   5000,
		ProductID:   strptr(productID),
		Ingredients: []models.RecipeIngredient{
			// Legacy line — ProductUUID set, IngredientID nil.
			{RecipeUUID: recipeID, ProductUUID: strptr("old-prod"), ProductName: "Pan", Quantity: 1},
		},
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Receta vieja", Price: 5000,
		IsRecipe: true, RecipeID: strptr(recipeID),
	}).Error)

	svc := services.NewRecipeService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID: tenantID, SaleUUID: "5a1e0000-0000-4000-8000-0000000000a3",
			ProductID: productID, Quantity: 1,
		})
	})
	require.NoError(t, err)
	var movCount int64
	db.Model(&models.InventoryMovement{}).Count(&movCount)
	assert.Equal(t, int64(0), movCount, "a legacy product-line must be skipped, not exploded")
}

// A recipe line pointing at a soft-deleted insumo is skipped so the
// venta is never lost (D3).
func TestExplodeRecipe_SkipsMissingIngredient(t *testing.T) {
	db := setupRecipeDB(t)
	tenantID := "tenant-a"
	productID := "p4000000-0000-4000-8000-000000000001"
	recipeID := "r4000000-0000-4000-8000-000000000001"
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Receta",
		SalePrice:   5000,
		ProductID:   strptr(productID),
		Ingredients: []models.RecipeIngredient{
			// IngredientID points at an insumo that does not exist.
			{RecipeUUID: recipeID, ProductName: "Fantasma", Quantity: 1,
				IngredientID: strptr("99000000-0000-4000-8000-000000000001")},
		},
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Receta", Price: 5000,
		IsRecipe: true, RecipeID: strptr(recipeID),
	}).Error)

	svc := services.NewRecipeService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID: tenantID, SaleUUID: "5a1e0000-0000-4000-8000-0000000000a4",
			ProductID: productID, Quantity: 1,
		})
	})
	require.NoError(t, err, "a missing insumo must not lose the venta")
}

// Zero / negative quantity is a no-op.
func TestExplodeRecipe_ZeroQuantityNoOp(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db)
	svc := services.NewRecipeService(db)
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID: f.tenantID, SaleUUID: "5a1e0000-0000-4000-8000-0000000000a5",
			ProductID: f.productID, Quantity: 0,
		})
	})
	require.NoError(t, err)
	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "qty 0 changes nothing")
}

// Art. III — a recipe belonging to another tenant is never exploded.
func TestExplodeRecipe_TenantIsolation(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db) // owned by tenant-a
	svc := services.NewRecipeService(db)

	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID:  "tenant-b", // different tenant
			SaleUUID:  "5a1e0000-0000-4000-8000-000000000005",
			ProductID: f.productID,
			Quantity:  1,
		})
	})
	require.NoError(t, err)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9,
		"tenant-b cannot explode tenant-a's recipe — stock untouched")
}
