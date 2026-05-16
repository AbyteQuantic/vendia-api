// Spec: specs/003-trabajos-muebles/spec.md
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

// setupWorkOrderDB migrates the schema CompleteWorkOrder touches:
// WorkOrder, WorkOrderItem, WorkOrderPayment, Ingredient, Product and
// InventoryMovement. None carry Postgres-only defaults, so AutoMigrate
// works on sqlite directly.
func setupWorkOrderDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.WorkOrder{},
		&models.WorkOrderItem{},
		&models.WorkOrderPayment{},
		&models.Ingredient{},
		&models.Product{},
		&models.InventoryMovement{},
	))
	return db
}

func woStrPtr(s string) *string { return &s }

// woFixture wires an `aprobada` work order with one insumo material
// line ("Madera" ×2, stock 10), one product material line ("Tornillo"
// ×5, stock 20) and one labour line.
type woFixture struct {
	tenantID   string
	woID       string
	customerID string
	maderaID   string
	tornilloID string
}

// seedApprovedWO seeds an `aprobada` work order ready to be completed.
func seedApprovedWO(t *testing.T, db *gorm.DB) woFixture {
	t.Helper()
	f := woFixture{
		tenantID:   "tenant-a",
		woID:       "70000000-0000-4000-8000-000000000001",
		customerID: "c0000000-0000-4000-8000-000000000001",
		maderaID:   "10000000-0000-4000-8000-000000000001",
		tornilloID: "20000000-0000-4000-8000-000000000001",
	}
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: f.maderaID},
		TenantID:  f.tenantID, Name: "Madera", Unit: models.UnitUnidad,
		Stock: 10, UnitCost: 20000,
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: f.tornilloID},
		TenantID:  f.tenantID, Name: "Tornillo", Price: 200, Stock: 20,
	}).Error)
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel:  models.BaseModel{ID: f.woID},
		TenantID:   f.tenantID,
		CustomerID: f.customerID,
		Type:       models.WorkOrderTypeFabrication,
		Status:     models.WorkOrderInProgress,
		Total:      95000,
		Items: []models.WorkOrderItem{
			{
				WorkOrderID: f.woID, Kind: models.WorkOrderItemMaterial,
				IngredientID: woStrPtr(f.maderaID), Description: "Madera",
				Quantity: 2, UnitPrice: 20000,
			},
			{
				WorkOrderID: f.woID, Kind: models.WorkOrderItemMaterial,
				ProductID: woStrPtr(f.tornilloID), Description: "Tornillo",
				Quantity: 5, UnitPrice: 200,
			},
			{
				WorkOrderID: f.woID, Kind: models.WorkOrderItemLabor,
				Description: "Mano de obra", Quantity: 1, UnitPrice: 50000,
			},
		},
	}).Error)
	return f
}

// AC-03 — completing a work order discounts every material item via a
// work_order_consumption kardex movement and flips it to terminada.
func TestCompleteWorkOrder_ConsumesMaterialAndCompletes(t *testing.T) {
	db := setupWorkOrderDB(t)
	f := seedApprovedWO(t, db)
	svc := services.NewWorkOrderService(db)

	wo, err := svc.CompleteWorkOrder(f.tenantID, f.woID, services.WorkOrderContext{})
	require.NoError(t, err)
	assert.Equal(t, models.WorkOrderCompleted, wo.Status)
	require.NotNil(t, wo.CompletedAt, "completed_at must be stamped")

	// Insumo: 10 - 2 = 8 (AC-03).
	var madera models.Ingredient
	require.NoError(t, db.First(&madera, "id = ?", f.maderaID).Error)
	assert.InDelta(t, 8.0, madera.Stock, 1e-9, "madera 10 - 2 = 8")

	// Product: 20 - 5 = 15.
	var tornillo models.Product
	require.NoError(t, db.First(&tornillo, "id = ?", f.tornilloID).Error)
	assert.Equal(t, 15, tornillo.Stock, "tornillo 20 - 5 = 15")

	// One work_order_consumption movement per material item — the
	// labour line moves no stock.
	var movements []models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementWorkOrderConsumption).
		Find(&movements).Error)
	require.Len(t, movements, 2, "one movement per material item, none for labour")
	for _, m := range movements {
		require.NotNil(t, m.ReferenceID)
		assert.Equal(t, f.woID, *m.ReferenceID, "movement anchored to the work order UUID")
		assert.Equal(t, "work_order", m.ReferenceType)
		assert.Less(t, m.Quantity, float64(0), "a consumption is an outgoing (negative) movement")
	}
}

// AC-04 / Art. II — completing the SAME work order twice is idempotent:
// stock does NOT change on the second call and no new movements appear.
func TestCompleteWorkOrder_IdempotentOnReComplete(t *testing.T) {
	db := setupWorkOrderDB(t)
	f := seedApprovedWO(t, db)
	svc := services.NewWorkOrderService(db)

	_, err := svc.CompleteWorkOrder(f.tenantID, f.woID, services.WorkOrderContext{})
	require.NoError(t, err)

	// Second complete — must be a safe no-op (re-sync, Art. II).
	wo2, err := svc.CompleteWorkOrder(f.tenantID, f.woID, services.WorkOrderContext{})
	require.NoError(t, err, "re-completing an already-terminada work order must not error")
	assert.Equal(t, models.WorkOrderCompleted, wo2.Status)

	var madera models.Ingredient
	require.NoError(t, db.First(&madera, "id = ?", f.maderaID).Error)
	assert.InDelta(t, 8.0, madera.Stock, 1e-9, "stock must NOT double-discount on re-complete")

	var tornillo models.Product
	require.NoError(t, db.First(&tornillo, "id = ?", f.tornilloID).Error)
	assert.Equal(t, 15, tornillo.Stock, "product stock must NOT double-discount")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementWorkOrderConsumption).
		Count(&movCount)
	assert.Equal(t, int64(2), movCount, "re-complete must not append duplicate movements")
}

// §9 — completing with stock insufficient is ALLOWED (the material was
// already used); the insumo goes negative and stays visible in kardex.
func TestCompleteWorkOrder_AllowsNegativeStock(t *testing.T) {
	db := setupWorkOrderDB(t)
	f := seedApprovedWO(t, db)
	// Drop madera stock below the 2 the order needs.
	require.NoError(t, db.Model(&models.Ingredient{}).
		Where("id = ?", f.maderaID).Update("stock", 1).Error)

	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder(f.tenantID, f.woID, services.WorkOrderContext{})
	require.NoError(t, err, "insufficient stock must not block completion (§9)")

	var madera models.Ingredient
	require.NoError(t, db.First(&madera, "id = ?", f.maderaID).Error)
	assert.InDelta(t, -1.0, madera.Stock, 1e-9, "1 - 2 = -1, negative stock visible in kardex")
}

// §9 — a material item referencing a soft-deleted insumo blocks
// completion (atomic: nothing moves).
func TestCompleteWorkOrder_BlocksOnDeletedReference(t *testing.T) {
	db := setupWorkOrderDB(t)
	f := seedApprovedWO(t, db)
	require.NoError(t, db.Delete(&models.Ingredient{}, "id = ?", f.maderaID).Error)

	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder(f.tenantID, f.woID, services.WorkOrderContext{})
	require.Error(t, err, "a work order referencing a deleted insumo cannot be completed")
	assert.ErrorIs(t, err, services.ErrWOItemInvalid)

	// Atomic: the still-valid product line must NOT have moved.
	var tornillo models.Product
	require.NoError(t, db.First(&tornillo, "id = ?", f.tornilloID).Error)
	assert.Equal(t, 20, tornillo.Stock, "a blocked completion is atomic — nothing moves")

	var wo models.WorkOrder
	require.NoError(t, db.First(&wo, "id = ?", f.woID).Error)
	assert.Equal(t, models.WorkOrderInProgress, wo.Status, "a blocked completion leaves status untouched")
}

// AC-05 — completing from an invalid state (cotizacion) is rejected.
func TestCompleteWorkOrder_RejectsInvalidTransition(t *testing.T) {
	db := setupWorkOrderDB(t)
	f := seedApprovedWO(t, db)
	require.NoError(t, db.Model(&models.WorkOrder{}).
		Where("id = ?", f.woID).Update("status", models.WorkOrderQuote).Error)

	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder(f.tenantID, f.woID, services.WorkOrderContext{})
	require.Error(t, err, "cotizacion → terminada is not a valid transition")
	assert.ErrorIs(t, err, services.ErrWONotCompletable)

	var madera models.Ingredient
	require.NoError(t, db.First(&madera, "id = ?", f.maderaID).Error)
	assert.InDelta(t, 10.0, madera.Stock, 1e-9, "a rejected completion must not move stock")
}

// Art. III — another tenant cannot complete this tenant's work order.
func TestCompleteWorkOrder_TenantIsolation(t *testing.T) {
	db := setupWorkOrderDB(t)
	f := seedApprovedWO(t, db)
	svc := services.NewWorkOrderService(db)

	_, err := svc.CompleteWorkOrder("tenant-b", f.woID, services.WorkOrderContext{})
	require.Error(t, err, "tenant-b must not complete tenant-a's work order")
	assert.ErrorIs(t, err, services.ErrWONotFound)

	var madera models.Ingredient
	require.NoError(t, db.First(&madera, "id = ?", f.maderaID).Error)
	assert.InDelta(t, 10.0, madera.Stock, 1e-9, "cross-tenant completion must not move stock")
}

// A work order that does not exist returns ErrWONotFound.
func TestCompleteWorkOrder_NotFound(t *testing.T) {
	db := setupWorkOrderDB(t)
	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder("tenant-a",
		"99999999-9999-4999-8999-999999999999", services.WorkOrderContext{})
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrWONotFound)
}

// A completion carrying branch / user metadata stamps it onto each
// kardex movement (offline-first traceability).
func TestCompleteWorkOrder_StampsBranchAndUser(t *testing.T) {
	db := setupWorkOrderDB(t)
	f := seedApprovedWO(t, db)
	branchID := "b0000000-0000-4000-8000-000000000001"
	userID := "d0000000-0000-4000-8000-000000000001"

	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder(f.tenantID, f.woID, services.WorkOrderContext{
		BranchID: &branchID,
		UserID:   &userID,
	})
	require.NoError(t, err)

	var movements []models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementWorkOrderConsumption).
		Find(&movements).Error)
	require.Len(t, movements, 2)
	for _, m := range movements {
		require.NotNil(t, m.BranchID)
		assert.Equal(t, branchID, *m.BranchID)
		require.NotNil(t, m.UserID)
		assert.Equal(t, userID, *m.UserID)
	}
}

// A product material line is discounted; re-completing is idempotent on
// the product path too (consumeItem catches the existing movement).
func TestCompleteWorkOrder_ProductOnlyIsIdempotent(t *testing.T) {
	db := setupWorkOrderDB(t)
	tenantID := "tenant-a"
	prodID := "20000000-0000-4000-8000-0000000000cc"
	woID := "70000000-0000-4000-8000-0000000000cc"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: prodID},
		TenantID:  tenantID, Name: "Bisagra", Price: 500, Stock: 12,
	}).Error)
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel:  models.BaseModel{ID: woID},
		TenantID:   tenantID,
		CustomerID: "c0000000-0000-4000-8000-0000000000cc",
		Type:       models.WorkOrderTypeRepair,
		Status:     models.WorkOrderInProgress,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: models.WorkOrderItemMaterial,
				ProductID: woStrPtr(prodID), Description: "Bisagra", Quantity: 4, UnitPrice: 500},
		},
	}).Error)

	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder(tenantID, woID, services.WorkOrderContext{})
	require.NoError(t, err)
	// Second complete — idempotent.
	_, err = svc.CompleteWorkOrder(tenantID, woID, services.WorkOrderContext{})
	require.NoError(t, err)

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", prodID).Error)
	assert.Equal(t, 8, prod.Stock, "12 - 4 = 8, not double-discounted")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementWorkOrderConsumption).Count(&movCount)
	assert.Equal(t, int64(1), movCount, "re-complete must not append a duplicate movement")
}

// §9 — a material item referencing a soft-deleted PRODUCT blocks the
// completion (atomic).
func TestCompleteWorkOrder_BlocksOnDeletedProduct(t *testing.T) {
	db := setupWorkOrderDB(t)
	tenantID := "tenant-a"
	prodID := "20000000-0000-4000-8000-0000000000dd"
	woID := "70000000-0000-4000-8000-0000000000dd"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: prodID},
		TenantID:  tenantID, Name: "Bisagra", Price: 500, Stock: 12,
	}).Error)
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel:  models.BaseModel{ID: woID},
		TenantID:   tenantID,
		CustomerID: "c0000000-0000-4000-8000-0000000000dd",
		Type:       models.WorkOrderTypeRepair,
		Status:     models.WorkOrderInProgress,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: models.WorkOrderItemMaterial,
				ProductID: woStrPtr(prodID), Description: "Bisagra", Quantity: 4, UnitPrice: 500},
		},
	}).Error)
	require.NoError(t, db.Delete(&models.Product{}, "id = ?", prodID).Error)

	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder(tenantID, woID, services.WorkOrderContext{})
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrWOItemInvalid)

	var wo models.WorkOrder
	require.NoError(t, db.First(&wo, "id = ?", woID).Error)
	assert.Equal(t, models.WorkOrderInProgress, wo.Status)
}

// A fractional product quantity is truncated for the integer stock
// column but the kardex movement records the exact figure.
func TestCompleteWorkOrder_TruncatesFractionalProductQuantity(t *testing.T) {
	db := setupWorkOrderDB(t)
	tenantID := "tenant-a"
	prodID := "20000000-0000-4000-8000-0000000000ee"
	woID := "70000000-0000-4000-8000-0000000000ee"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: prodID},
		TenantID:  tenantID, Name: "Tela", Price: 1000, Stock: 10,
	}).Error)
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel:  models.BaseModel{ID: woID},
		TenantID:   tenantID,
		CustomerID: "c0000000-0000-4000-8000-0000000000ee",
		Type:       models.WorkOrderTypeFabrication,
		Status:     models.WorkOrderInProgress,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: models.WorkOrderItemMaterial,
				ProductID: woStrPtr(prodID), Description: "Tela", Quantity: 2.7, UnitPrice: 1000},
		},
	}).Error)

	svc := services.NewWorkOrderService(db)
	_, err := svc.CompleteWorkOrder(tenantID, woID, services.WorkOrderContext{})
	require.NoError(t, err)

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", prodID).Error)
	assert.Equal(t, 8, prod.Stock, "product stock is integer — 10 - int(2.7) = 8")

	var mov models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementWorkOrderConsumption).
		First(&mov).Error)
	assert.InDelta(t, -2.7, mov.Quantity, 1e-9, "the kardex keeps the exact figure")
}

// A work order with only labour items completes with no kardex movement.
func TestCompleteWorkOrder_LabourOnlyNoMovements(t *testing.T) {
	db := setupWorkOrderDB(t)
	woID := "70000000-0000-4000-8000-0000000000bb"
	require.NoError(t, db.Create(&models.WorkOrder{
		BaseModel:  models.BaseModel{ID: woID},
		TenantID:   "tenant-a",
		CustomerID: "c0000000-0000-4000-8000-0000000000bb",
		Type:       models.WorkOrderTypeRepair,
		Status:     models.WorkOrderInProgress,
		Items: []models.WorkOrderItem{
			{WorkOrderID: woID, Kind: models.WorkOrderItemLabor,
				Description: "Reparar bisagra", Quantity: 1, UnitPrice: 30000},
		},
	}).Error)

	svc := services.NewWorkOrderService(db)
	wo, err := svc.CompleteWorkOrder("tenant-a", woID, services.WorkOrderContext{})
	require.NoError(t, err)
	assert.Equal(t, models.WorkOrderCompleted, wo.Status)

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementWorkOrderConsumption).Count(&movCount)
	assert.Equal(t, int64(0), movCount, "a labour-only work order moves no stock")
}
