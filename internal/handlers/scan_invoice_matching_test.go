// Spec: specs/099-inventario-voz-factura-campos-separados/spec.md
//
// Characterizes ScanInvoice's coincidence-matching behavior BEFORE
// refactoring it to call the shared services.MatchProducts (Spec 099
// FR-05, plan.md §5.D) — these tests must keep passing unmodified after
// the refactor lands; that's the proof the refactor changed no
// observable behavior (AC-05).
package handlers

import (
	"context"
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupScanInvoiceMatchingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	return db
}

// TestBuildInvoiceProductResults_BarcodeMatch verifies level 1: an
// invoice line with a barcode that matches an existing product is
// tagged match_encontrado/barcode.
func TestBuildInvoiceProductResults_BarcodeMatch(t *testing.T) {
	db := setupScanInvoiceMatchingDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", Name: "Speed Max", Barcode: "7501234567890",
		Price: 2500, Stock: 5, IsAvailable: true,
	}).Error)

	items := []services.InvoiceProduct{
		{Name: "Speed Max", Presentation: "PET", Content: "250ml", Quantity: 12, Barcode: "7501234567890", Confidence: 0.95},
	}
	results := buildInvoiceProductResults(context.Background(), db, nil, "t1", "", items, nil)

	require.Len(t, results, 1)
	assert.Equal(t, "match_encontrado", results[0].Status)
	assert.Equal(t, "barcode", results[0].MatchMethod)
	assert.NotEmpty(t, results[0].MatchProductID)
}

// TestBuildInvoiceProductResults_NormalizedMatch verifies level 2 when
// no barcode is present. The matching key compares the QUERY's
// name+content (displayName, e.g. "Arroz Diana 500g") against the
// EXISTING product's Name field — so a realistic existing row (as
// ScanInvoice itself would have created it earlier: content already
// folded into Name, per the "append content to name" display
// convention) has Name="Arroz Diana 500g", not "Arroz Diana".
func TestBuildInvoiceProductResults_NormalizedMatch(t *testing.T) {
	db := setupScanInvoiceMatchingDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", Name: "Arroz Diana 500g", Presentation: "bolsa", Content: "500g",
		Price: 3000, Stock: 10, IsAvailable: true,
	}).Error)

	items := []services.InvoiceProduct{
		{Name: "Arroz Diana", Presentation: "bolsa", Content: "500g", Quantity: 1, Confidence: 0.9},
	}
	results := buildInvoiceProductResults(context.Background(), db, nil, "t1", "", items, nil)

	require.Len(t, results, 1)
	assert.Equal(t, "match_encontrado", results[0].Status)
	assert.Equal(t, "normalized", results[0].MatchMethod)
}

// TestBuildInvoiceProductResults_NoMatch_StatusNuevo verifies a
// genuinely new item is tagged "nuevo" (the pre-refactor behavior:
// the "precio_pendiente" initial value is always overwritten by the
// final match/no-match decision).
func TestBuildInvoiceProductResults_NoMatch_StatusNuevo(t *testing.T) {
	db := setupScanInvoiceMatchingDB(t)

	items := []services.InvoiceProduct{
		{Name: "Producto Nuevo De Factura", Quantity: 1, Confidence: 0.8},
	}
	results := buildInvoiceProductResults(context.Background(), db, nil, "t1", "", items, nil)

	require.Len(t, results, 1)
	assert.Equal(t, "nuevo", results[0].Status)
}

// TestBuildInvoiceProductResults_ContentAppendedToDisplayName verifies
// the existing "Coca Cola" + "1.5L" → "Coca Cola 1.5L" display-name
// behavior survives the refactor.
func TestBuildInvoiceProductResults_ContentAppendedToDisplayName(t *testing.T) {
	db := setupScanInvoiceMatchingDB(t)

	items := []services.InvoiceProduct{
		{Name: "Coca Cola", Content: "1.5L", Quantity: 6, Confidence: 0.9},
	}
	results := buildInvoiceProductResults(context.Background(), db, nil, "t1", "", items, nil)

	require.Len(t, results, 1)
	assert.Equal(t, "Coca Cola 1.5L", results[0].Name)
}

// TestBuildInvoiceProductResults_BranchScoped_NoCrossBranchMatch
// verifies Art. III branch isolation survives the refactor to the
// shared matcher.
func TestBuildInvoiceProductResults_BranchScoped_NoCrossBranchMatch(t *testing.T) {
	db := setupScanInvoiceMatchingDB(t)
	branchA := "branch-a"
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", BranchID: &branchA, Name: "Producto Sede A",
		Barcode: "9999999999999", Price: 1000, Stock: 1, IsAvailable: true,
	}).Error)

	items := []services.InvoiceProduct{
		{Name: "Producto Sede A", Barcode: "9999999999999", Quantity: 1, Confidence: 0.9},
	}
	results := buildInvoiceProductResults(context.Background(), db, nil, "t1", "branch-b", items, nil)

	require.Len(t, results, 1)
	assert.Equal(t, "nuevo", results[0].Status, "branch-b must not match branch-a's product")
}

// TestBuildInvoiceProductResults_SupplierIDPropagated verifies a
// matched supplier ID (resolved upstream from the invoice provider
// name) is copied onto every product result, matched or new.
func TestBuildInvoiceProductResults_SupplierIDPropagated(t *testing.T) {
	db := setupScanInvoiceMatchingDB(t)
	supplierID := "supplier-123"

	items := []services.InvoiceProduct{{Name: "Cualquier Producto", Quantity: 1, Confidence: 0.9}}
	results := buildInvoiceProductResults(context.Background(), db, nil, "t1", "", items, &supplierID)

	require.Len(t, results, 1)
	assert.Equal(t, supplierID, results[0].SupplierID)
}

// TestBuildInvoiceProductResults_InvalidExpiryDate_DroppedNotFailed
// verifies a bad expiry_date string never reaches the response as a
// bogus date — existing normaliseExpiryDate contract preserved.
func TestBuildInvoiceProductResults_InvalidExpiryDate_DroppedNotFailed(t *testing.T) {
	db := setupScanInvoiceMatchingDB(t)

	items := []services.InvoiceProduct{
		{Name: "Producto Con Fecha Mala", Quantity: 1, ExpiryDate: "no-es-una-fecha", Confidence: 0.9},
	}
	results := buildInvoiceProductResults(context.Background(), db, nil, "t1", "", items, nil)

	require.Len(t, results, 1)
	assert.Empty(t, results[0].ExpiryDate)
}
