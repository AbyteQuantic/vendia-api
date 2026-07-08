// Spec: specs/099-inventario-voz-factura-campos-separados/spec.md
package handlers

import (
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupVoiceMatchingDB creates an in-memory SQLite database with the
// products table — buildVoiceInventoryResults is a pure function (no
// Gemini call inside), so it's directly testable without any network
// or API-key dependency, unlike the full VoiceInventory HTTP handler.
func setupVoiceMatchingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	return db
}

// TestBuildVoiceInventoryResults_BarcodeMatch_ReturnsMatchInfo verifies
// AC-02: an item that matches an existing product by barcode comes back
// tagged so the review screen can offer "sumar stock" instead of
// creating a duplicate.
func TestBuildVoiceInventoryResults_BarcodeMatch_ReturnsMatchInfo(t *testing.T) {
	db := setupVoiceMatchingDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", Name: "Coca-Cola 350ml", Barcode: "7702090000012",
		Price: 2500, Stock: 10, IsAvailable: true,
	}).Error)

	items := []services.VoiceInventoryItem{
		{Name: "Coca-Cola", Content: "350ml", Quantity: 20, PurchasePrice: 3000, SellPrice: 3500, Barcode: "7702090000012"},
	}
	results := buildVoiceInventoryResults(db, "t1", "", items)

	require.Len(t, results, 1)
	assert.Equal(t, "match_encontrado", results[0].Status)
	assert.Equal(t, "barcode", results[0].MatchMethod)
	assert.NotEmpty(t, results[0].MatchProductID)
	// The dictated fields must survive untouched alongside the match info.
	assert.Equal(t, "Coca-Cola", results[0].Name)
	assert.Equal(t, "350ml", results[0].Content)
	assert.EqualValues(t, 20, results[0].Quantity)
	assert.EqualValues(t, 3000, results[0].PurchasePrice)
	assert.EqualValues(t, 3500, results[0].SellPrice)
}

// TestBuildVoiceInventoryResults_NoMatch_ReturnsNuevo verifies a
// genuinely new product (no barcode/name match) is tagged "nuevo".
func TestBuildVoiceInventoryResults_NoMatch_ReturnsNuevo(t *testing.T) {
	db := setupVoiceMatchingDB(t)

	items := []services.VoiceInventoryItem{
		{Name: "Producto Completamente Nuevo XYZ", Quantity: 5, PurchasePrice: 1000},
	}
	results := buildVoiceInventoryResults(db, "t1", "", items)

	require.Len(t, results, 1)
	assert.Equal(t, "nuevo", results[0].Status)
	assert.Empty(t, results[0].MatchProductID)
}

// TestBuildVoiceInventoryResults_BranchScoped_NoCrossBranchMatch verifies
// Art. III: a product from another branch never counts as a match.
func TestBuildVoiceInventoryResults_BranchScoped_NoCrossBranchMatch(t *testing.T) {
	db := setupVoiceMatchingDB(t)
	branchA := "branch-a"
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", BranchID: &branchA, Name: "Arroz X", Barcode: "1111111111111",
		Price: 3000, Stock: 5, IsAvailable: true,
	}).Error)

	items := []services.VoiceInventoryItem{{Name: "Arroz X", Barcode: "1111111111111", Quantity: 3}}
	results := buildVoiceInventoryResults(db, "t1", "branch-b", items)

	require.Len(t, results, 1)
	assert.Equal(t, "nuevo", results[0].Status, "branch-b must not see branch-a's product")
}

// TestBuildVoiceInventoryResults_MultipleItems_PreservesOrder verifies
// each item's match is computed independently and order is preserved —
// a wrong index here would show the wrong "producto ya existe" banner
// on the wrong row in the review screen.
func TestBuildVoiceInventoryResults_MultipleItems_PreservesOrder(t *testing.T) {
	db := setupVoiceMatchingDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", Name: "Leche Entera", Barcode: "2222222222222",
		Price: 4000, Stock: 8, IsAvailable: true,
	}).Error)

	items := []services.VoiceInventoryItem{
		{Name: "Producto Nuevo Uno", Quantity: 1},
		{Name: "Leche Entera", Barcode: "2222222222222", Quantity: 10},
		{Name: "Producto Nuevo Dos", Quantity: 2},
	}
	results := buildVoiceInventoryResults(db, "t1", "", items)

	require.Len(t, results, 3)
	assert.Equal(t, "nuevo", results[0].Status)
	assert.Equal(t, "match_encontrado", results[1].Status)
	assert.Equal(t, "nuevo", results[2].Status)
}
