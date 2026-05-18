// Spec: specs/014-inventario-solido-scope-sede/spec.md
package handlers

import (
	"testing"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupScopeDB migrates a Product table for ApplyBranchScope tests.
func setupScopeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	return db
}

// TestApplyBranchScope_IncludesNullBranchRows verifies FR-06 / D5: a
// sede-scoped query must still surface a product whose branch_id is NULL
// — the scope is a safety net, never a row-hider. Without the
// `OR branch_id IS NULL` clause a legacy NULL product would vanish from
// Inventario even after the sede selector is used.
func TestApplyBranchScope_IncludesNullBranchRows(t *testing.T) {
	db := setupScopeDB(t)
	tenantID := uuid.NewString()
	branchID := uuid.NewString()

	scopedID := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: scopedID},
		TenantID:  tenantID, BranchID: &branchID,
		Name: "Producto con sede", Price: 1000, IsAvailable: true,
	}).Error)

	nullID := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: nullID},
		TenantID:  tenantID,
		Name:      "Producto sin sede", Price: 1000, IsAvailable: true,
	}).Error)

	scope := BranchScopeResolution{BranchID: branchID}
	var products []models.Product
	require.NoError(t, ApplyBranchScope(
		db.Model(&models.Product{}).Where("tenant_id = ?", tenantID), scope,
	).Find(&products).Error)

	ids := map[string]bool{}
	for _, p := range products {
		ids[p.ID] = true
	}
	assert.True(t, ids[scopedID], "el producto de la sede aparece")
	assert.True(t, ids[nullID],
		"un producto con branch_id NULL NUNCA queda invisible bajo el scope")
	assert.Len(t, products, 2)
}

// TestApplyBranchScope_ExcludesOtherBranches verifies no regression: a
// product that lives in a DIFFERENT sede is still filtered out.
func TestApplyBranchScope_ExcludesOtherBranches(t *testing.T) {
	db := setupScopeDB(t)
	tenantID := uuid.NewString()
	branchA := uuid.NewString()
	branchB := uuid.NewString()

	inA := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: inA},
		TenantID:  tenantID, BranchID: &branchA,
		Name: "Sede A", Price: 1000, IsAvailable: true,
	}).Error)
	inB := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: inB},
		TenantID:  tenantID, BranchID: &branchB,
		Name: "Sede B", Price: 1000, IsAvailable: true,
	}).Error)

	scope := BranchScopeResolution{BranchID: branchA}
	var products []models.Product
	require.NoError(t, ApplyBranchScope(
		db.Model(&models.Product{}).Where("tenant_id = ?", tenantID), scope,
	).Find(&products).Error)

	require.Len(t, products, 1, "solo la fila de la sede A (la otra sede se filtra)")
	assert.Equal(t, inA, products[0].ID)
}

// TestApplyBranchScope_NoScopeReturnsAll verifies the "no scope" path is
// untouched — an empty resolution leaves the query as-is.
func TestApplyBranchScope_NoScopeReturnsAll(t *testing.T) {
	db := setupScopeDB(t)
	tenantID := uuid.NewString()
	branchID := uuid.NewString()

	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		TenantID:  tenantID, BranchID: &branchID,
		Name: "Con sede", Price: 1000, IsAvailable: true,
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		TenantID:  tenantID,
		Name:      "Sin sede", Price: 1000, IsAvailable: true,
	}).Error)

	var products []models.Product
	require.NoError(t, ApplyBranchScope(
		db.Model(&models.Product{}).Where("tenant_id = ?", tenantID),
		BranchScopeResolution{},
	).Find(&products).Error)

	assert.Len(t, products, 2, "sin scope se devuelven todas las filas del tenant")
}
