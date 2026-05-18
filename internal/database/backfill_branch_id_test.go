// Spec: specs/014-inventario-solido-scope-sede/spec.md
package database

import (
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupBackfillDB builds an in-memory SQLite schema with the tables
// BackfillBranchIDs touches. The real Tenant model can't AutoMigrate on
// SQLite (jsonb column), so the tenants table is hand-crafted; every
// other table is migrated from its real GORM model.
func setupBackfillDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		);
	`).Error)
	require.NoError(t, db.AutoMigrate(
		&models.Branch{},
		&models.Product{},
		&models.Sale{},
		&models.InventoryMovement{},
		&models.CreditAccount{},
		&models.OrderTicket{},
	))
	return db
}

func insertBackfillTenant(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, created_at, updated_at) VALUES (?, ?, ?)`,
		id, time.Now(), time.Now()).Error)
}

// seedBranch inserts a branch and returns its id. createdAt drives the
// "oldest = default" tie-breaker.
func seedBranch(t *testing.T, db *gorm.DB, tenantID string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.Branch{
		BaseModel: models.BaseModel{ID: id, CreatedAt: createdAt},
		TenantID:  tenantID,
		Name:      "Sede " + id[:4],
		IsActive:  true,
	}).Error)
	return id
}

// TestBackfillBranchIDs_AssignsDefaultBranch verifies AC-01: an
// operational row with branch_id NULL receives the tenant's default
// sede (the oldest non-deleted branch).
func TestBackfillBranchIDs_AssignsDefaultBranch(t *testing.T) {
	db := setupBackfillDB(t)
	tenantID := uuid.NewString()
	insertBackfillTenant(t, db, tenantID)

	now := time.Now()
	oldBranch := seedBranch(t, db, tenantID, now.Add(-48*time.Hour))
	_ = seedBranch(t, db, tenantID, now.Add(-1*time.Hour)) // newer — must NOT win

	// One NULL-branch row per covered table.
	prodID := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: prodID},
		TenantID:  tenantID, Name: "Llaveros", Price: 2000,
	}).Error)
	saleID := uuid.NewString()
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{ID: saleID},
		TenantID:  tenantID, Total: 5000,
	}).Error)
	movID := uuid.NewString()
	require.NoError(t, db.Create(&models.InventoryMovement{
		ID: movID, TenantID: tenantID, ProductID: prodID,
		MovementType: models.MovementInitialStock, Quantity: 1,
	}).Error)
	creditID := uuid.NewString()
	require.NoError(t, db.Create(&models.CreditAccount{
		BaseModel: models.BaseModel{ID: creditID},
		TenantID:  tenantID, CustomerID: uuid.NewString(), TotalAmount: 1000,
	}).Error)
	ticketID := uuid.NewString()
	require.NoError(t, db.Create(&models.OrderTicket{
		BaseModel: models.BaseModel{ID: ticketID},
		TenantID:  tenantID, Label: "Mesa 1",
		Status: models.OrderStatusNuevo, Type: models.OrderTypeMesa,
	}).Error)

	touched, err := BackfillBranchIDs(db)
	require.NoError(t, err)
	assert.Equal(t, 5, touched, "una fila NULL por cada una de las 5 tablas")

	// Every row now points at the oldest branch.
	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", prodID).Error)
	require.NotNil(t, prod.BranchID)
	assert.Equal(t, oldBranch, *prod.BranchID, "el producto recibe la sede más antigua")

	var sale models.Sale
	require.NoError(t, db.First(&sale, "id = ?", saleID).Error)
	require.NotNil(t, sale.BranchID)
	assert.Equal(t, oldBranch, *sale.BranchID)

	var mov models.InventoryMovement
	require.NoError(t, db.First(&mov, "id = ?", movID).Error)
	require.NotNil(t, mov.BranchID)
	assert.Equal(t, oldBranch, *mov.BranchID)

	var credit models.CreditAccount
	require.NoError(t, db.First(&credit, "id = ?", creditID).Error)
	require.NotNil(t, credit.BranchID)
	assert.Equal(t, oldBranch, *credit.BranchID)

	var ticket models.OrderTicket
	require.NoError(t, db.First(&ticket, "id = ?", ticketID).Error)
	require.NotNil(t, ticket.BranchID)
	assert.Equal(t, oldBranch, *ticket.BranchID)
}

// TestBackfillBranchIDs_Idempotent verifies the backfill is a no-op on a
// second run — once every live row carries a branch_id, re-running it
// touches nothing.
func TestBackfillBranchIDs_Idempotent(t *testing.T) {
	db := setupBackfillDB(t)
	tenantID := uuid.NewString()
	insertBackfillTenant(t, db, tenantID)
	branchID := seedBranch(t, db, tenantID, time.Now().Add(-time.Hour))

	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		TenantID:  tenantID, Name: "Arroz", Price: 3000,
	}).Error)

	first, err := BackfillBranchIDs(db)
	require.NoError(t, err)
	assert.Equal(t, 1, first, "la primera corrida asigna la sede")

	second, err := BackfillBranchIDs(db)
	require.NoError(t, err)
	assert.Equal(t, 0, second, "una segunda corrida es no-op")

	// The branch assigned on the first run stays put.
	var prod models.Product
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&prod).Error)
	require.NotNil(t, prod.BranchID)
	assert.Equal(t, branchID, *prod.BranchID)
}

// TestBackfillBranchIDs_SkipsTenantWithoutBranch verifies a tenant with
// no sede at all is left untouched — there is nothing to assign, and we
// must not crash or assign another tenant's branch.
func TestBackfillBranchIDs_SkipsTenantWithoutBranch(t *testing.T) {
	db := setupBackfillDB(t)
	tenantNoBranch := uuid.NewString()
	insertBackfillTenant(t, db, tenantNoBranch)

	prodID := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: prodID},
		TenantID:  tenantNoBranch, Name: "Sin sede", Price: 1000,
	}).Error)

	touched, err := BackfillBranchIDs(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched, "un tenant sin sede no recibe backfill")

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", prodID).Error)
	assert.Nil(t, prod.BranchID, "el producto sigue sin sede — no se inventa una")
}

// TestBackfillBranchIDs_OnlyTouchesNullRows verifies a row that already
// carries a branch_id is never re-pointed at the default sede.
func TestBackfillBranchIDs_OnlyTouchesNullRows(t *testing.T) {
	db := setupBackfillDB(t)
	tenantID := uuid.NewString()
	insertBackfillTenant(t, db, tenantID)
	defaultBranch := seedBranch(t, db, tenantID, time.Now().Add(-48*time.Hour))
	otherBranch := seedBranch(t, db, tenantID, time.Now().Add(-time.Hour))

	// Already-scoped row pointing at the NON-default branch.
	scopedID := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: scopedID},
		TenantID:  tenantID, BranchID: &otherBranch, Name: "Ya con sede", Price: 1000,
	}).Error)
	// NULL row that should pick the default.
	nullID := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: nullID},
		TenantID:  tenantID, Name: "Sin sede", Price: 1000,
	}).Error)

	touched, err := BackfillBranchIDs(db)
	require.NoError(t, err)
	assert.Equal(t, 1, touched, "solo la fila NULL se toca")

	var scoped models.Product
	require.NoError(t, db.First(&scoped, "id = ?", scopedID).Error)
	require.NotNil(t, scoped.BranchID)
	assert.Equal(t, otherBranch, *scoped.BranchID,
		"una fila ya scopeada conserva su sede original")

	var nullRow models.Product
	require.NoError(t, db.First(&nullRow, "id = ?", nullID).Error)
	require.NotNil(t, nullRow.BranchID)
	assert.Equal(t, defaultBranch, *nullRow.BranchID)
}

// TestBackfillBranchIDs_SkipsSoftDeletedRows verifies a soft-deleted row
// is never backfilled — the backfill only repairs live data.
func TestBackfillBranchIDs_SkipsSoftDeletedRows(t *testing.T) {
	db := setupBackfillDB(t)
	tenantID := uuid.NewString()
	insertBackfillTenant(t, db, tenantID)
	seedBranch(t, db, tenantID, time.Now().Add(-time.Hour))

	deletedID := uuid.NewString()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: deletedID},
		TenantID:  tenantID, Name: "Borrado", Price: 1000,
	}).Error)
	require.NoError(t, db.Delete(&models.Product{}, "id = ?", deletedID).Error)

	touched, err := BackfillBranchIDs(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched, "una fila soft-deleted no se backfillea")

	var deleted models.Product
	require.NoError(t, db.Unscoped().First(&deleted, "id = ?", deletedID).Error)
	assert.Nil(t, deleted.BranchID, "la fila borrada sigue con branch_id NULL")
}

// TestBackfillBranchIDs_NoData verifies the function is safe to run on an
// empty database — zero rows, no error.
func TestBackfillBranchIDs_NoData(t *testing.T) {
	db := setupBackfillDB(t)
	touched, err := BackfillBranchIDs(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched)
}
