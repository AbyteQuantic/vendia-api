// Spec: specs/001-insumos-recetas/spec.md
package services_test

import (
	"testing"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSyncRecipeDB migrates the schema SyncService.ProcessBatch needs
// for a `sale` op that triggers recipe explosion.
func setupSyncRecipeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{},
		&models.Product{},
		&models.Sale{},
		&models.SaleItem{},
		&models.Customer{},
		&models.CreditAccount{},
		&models.CreditPayment{},
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Ingredient{},
		&models.InventoryMovement{},
	))
	return db
}

// AC-04 + Art. II — a sale synced through /sync/batch explodes the
// recipe of its product-receta line, AND re-syncing the SAME sale does
// not discount the insumos twice (offline re-sync idempotency).
func TestSyncBatch_RecipeSale_ExplodesOnceAcrossResync(t *testing.T) {
	db := setupSyncRecipeDB(t)
	tenantID := "11111111-1111-4111-8111-111111111111"
	productID := "a0000000-0000-4000-8000-000000000020"
	recipeID := "b0000000-0000-4000-8000-000000000020"
	arrozID := "c0000000-0000-4000-8000-000000000020"
	saleID := "d0000000-0000-4000-8000-000000000020"
	itemID := "e0000000-0000-4000-8000-000000000020"

	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: tenantID},
	}).Error)
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: arrozID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg, Stock: 3,
	}).Error)
	aid := arrozID
	pid := productID
	rid := recipeID
	require.NoError(t, db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: recipeID},
		TenantID:    tenantID,
		ProductName: "Almuerzo corriente",
		SalePrice:   12000,
		ProductID:   &pid,
		Ingredients: []models.RecipeIngredient{
			{RecipeUUID: rid, ProductName: "Arroz", Quantity: 0.15, IngredientID: &aid},
		},
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Almuerzo corriente",
		Price: 12000, IsRecipe: true, RecipeID: &rid,
	}).Error)

	// The sale row + its item are pre-seeded; the sync op only needs
	// to "create" the Sale (syncEntity sees it exists → applies LWW
	// branch, which still counts as applied). To exercise the create
	// path cleanly we instead let the op create the sale and seed the
	// item so Preload("Items") finds it.
	require.NoError(t, db.Create(&models.SaleItem{
		BaseModel: models.BaseModel{ID: itemID},
		SaleID:    saleID,
		ProductID: &pid,
		Name:      "Almuerzo corriente",
		Price:     12000,
		Quantity:  2,
		Subtotal:  24000,
	}).Error)

	svc := services.NewSyncService(db)
	now := time.Now()
	op := services.SyncOperation{
		Entity:          "sale",
		Action:          "create",
		ID:              saleID,
		ClientUpdatedAt: now,
		Data: map[string]any{
			"tenant_id":      tenantID,
			"total":          24000,
			"payment_method": "cash",
			// Real clients ship timestamps; the sync LWW path scans
			// updated_at on a re-sync, so the row must carry one.
			"created_at": now,
			"updated_at": now,
		},
	}
	req := services.SyncRequest{Operations: []services.SyncOperation{op}}

	// First sync — creates the sale and explodes the recipe.
	_, err := svc.ProcessBatch(tenantID, req)
	require.NoError(t, err)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", arrozID).Error)
	assert.InDelta(t, 2.70, arroz.Stock, 1e-9, "arroz: 3 - 2*0.15 = 2.70")

	// Re-sync the very same sale — must NOT discount again.
	_, err = svc.ProcessBatch(tenantID, req)
	require.NoError(t, err)

	require.NoError(t, db.First(&arroz, "id = ?", arrozID).Error)
	assert.InDelta(t, 2.70, arroz.Stock, 1e-9,
		"re-syncing the same sale must not double-discount the insumo")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).
		Count(&movCount)
	assert.Equal(t, int64(1), movCount,
		"exactly one recipe_consumption movement across the re-sync")
}
