// Spec: specs/099-inventario-voz-factura-campos-separados/spec.md
package services

import (
	"testing"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupProductMatcherDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	return db
}

// TestMatchProducts_BarcodeExact_IgnoresBranchWhenUnspecified verifies the
// existing (pre-099) behavior is preserved: an empty branchID never
// filters — callers who don't pass one (e.g. the currently-unused
// MatchProductsHandler) keep seeing every branch, exactly like today.
func TestMatchProducts_BarcodeExact_IgnoresBranchWhenUnspecified(t *testing.T) {
	db := setupProductMatcherDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", BranchID: strPtr("branch-a"), Name: "Coca-Cola 350ml",
		Barcode: "7702090000012", Price: 2500, Stock: 10, IsAvailable: true,
	}).Error)

	results := MatchProducts(db, "t1", []MatchProductRequest{{Barcode: "7702090000012"}}, "")
	require.Len(t, results, 1)
	require.Len(t, results[0], 1)
	assert.Equal(t, "barcode", results[0][0].MatchMethod)
	assert.Equal(t, 1.0, results[0][0].Confidence)
}

// TestMatchProducts_BarcodeExact_BranchScoped_NoCrossBranchMatch verifies
// Art. III: a product in branch A must never match a lookup scoped to
// branch B, even with an identical barcode — the same isolation
// ScanInvoice already relies on inline before this refactor.
func TestMatchProducts_BarcodeExact_BranchScoped_NoCrossBranchMatch(t *testing.T) {
	db := setupProductMatcherDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", BranchID: strPtr("branch-a"), Name: "Coca-Cola 350ml",
		Barcode: "7702090000012", Price: 2500, Stock: 10, IsAvailable: true,
	}).Error)

	results := MatchProducts(db, "t1", []MatchProductRequest{{Barcode: "7702090000012"}}, "branch-b")
	require.Len(t, results, 1)
	assert.Empty(t, results[0], "a branch-b lookup must not match a branch-a product")
}

// TestMatchProducts_BarcodeExact_BranchScoped_MatchesSameBranch verifies
// the positive case of the branch-scoping added in this spec.
func TestMatchProducts_BarcodeExact_BranchScoped_MatchesSameBranch(t *testing.T) {
	db := setupProductMatcherDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", BranchID: strPtr("branch-a"), Name: "Coca-Cola 350ml",
		Barcode: "7702090000012", Price: 2500, Stock: 10, IsAvailable: true,
	}).Error)

	results := MatchProducts(db, "t1", []MatchProductRequest{{Barcode: "7702090000012"}}, "branch-a")
	require.Len(t, results, 1)
	require.Len(t, results[0], 1)
	assert.Equal(t, "barcode", results[0][0].MatchMethod)
}

// TestMatchProducts_NormalizedNameMatch_BranchScoped verifies level 2
// (normalized name+presentation+content) also respects branch scope.
func TestMatchProducts_NormalizedNameMatch_BranchScoped(t *testing.T) {
	db := setupProductMatcherDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", BranchID: strPtr("branch-a"), Name: "Arroz Diana",
		Presentation: "bolsa", Content: "500g", Price: 3000, Stock: 5, IsAvailable: true,
	}).Error)

	req := []MatchProductRequest{{Name: "arroz diana", Presentation: "BOLSA", Content: "500G"}}

	sameBranch := MatchProducts(db, "t1", req, "branch-a")
	require.Len(t, sameBranch[0], 1)
	assert.Equal(t, "normalized", sameBranch[0][0].MatchMethod)

	otherBranch := MatchProducts(db, "t1", req, "branch-b")
	assert.Empty(t, otherBranch[0], "normalized match must also respect branch scope")
}

// TestMatchProducts_NoMatch_ReturnsEmptyCandidates verifies a genuinely
// new product returns no candidates at any level (barcode/normalized —
// fuzzy is not exercised here since SQLite has no pg_trgm; verified
// manually in production per Art. XII).
func TestMatchProducts_NoMatch_ReturnsEmptyCandidates(t *testing.T) {
	db := setupProductMatcherDB(t)
	require.NoError(t, db.Create(&models.Product{
		TenantID: "t1", Name: "Leche Alpina", Price: 4000, Stock: 5, IsAvailable: true,
	}).Error)

	results := MatchProducts(db, "t1", []MatchProductRequest{{Name: "Producto Completamente Distinto XYZ"}}, "")
	require.Len(t, results, 1)
	assert.Empty(t, results[0])
}

// ── BestMatchStatus ──────────────────────────────────────────────────

func TestBestMatchStatus_NoCandidates_ReturnsNuevo(t *testing.T) {
	status, id, method := BestMatchStatus(nil)
	assert.Equal(t, "nuevo", status)
	assert.Empty(t, id)
	assert.Empty(t, method)
}

func TestBestMatchStatus_PicksHighestConfidenceCandidate(t *testing.T) {
	candidates := []MatchCandidate{
		{ProductID: "p-fuzzy", Confidence: 0.65, MatchMethod: "fuzzy"},
		{ProductID: "p-barcode", Confidence: 1.0, MatchMethod: "barcode"},
		{ProductID: "p-normalized", Confidence: 0.9, MatchMethod: "normalized"},
	}
	status, id, method := BestMatchStatus(candidates)
	assert.Equal(t, "match_encontrado", status)
	assert.Equal(t, "p-barcode", id)
	assert.Equal(t, "barcode", method)
}

func TestBestMatchStatus_SingleFuzzyCandidate(t *testing.T) {
	candidates := []MatchCandidate{
		{ProductID: "p-fuzzy", Confidence: 0.72, MatchMethod: "fuzzy"},
	}
	status, id, method := BestMatchStatus(candidates)
	assert.Equal(t, "match_encontrado", status)
	assert.Equal(t, "p-fuzzy", id)
	assert.Equal(t, "fuzzy", method)
}
