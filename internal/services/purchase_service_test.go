// Spec: specs/002-ordenes-compra/spec.md
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

// setupPurchaseDB migrates the schema PurchaseService.ReceivePurchaseOrder
// touches: PurchaseOrder, PurchaseOrderItem, Ingredient, Product and
// InventoryMovement. None carry Postgres-only defaults, so AutoMigrate
// works on sqlite directly.
func setupPurchaseDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.PurchaseOrder{},
		&models.PurchaseOrderItem{},
		&models.Ingredient{},
		&models.Product{},
		&models.InventoryMovement{},
	))
	return db
}

func purStrPtr(s string) *string { return &s }

// purchaseFixture wires a PO with one insumo line and one product line.
type purchaseFixture struct {
	tenantID   string
	poID       string
	supplierID string
	arrozID    string
	gaseosaID  string
}

// seedSentPO seeds an `enviada` PO with: an Arroz insumo (stock 3,
// cost 2900) ordered 10 kg at 2900, and a Gaseosa product (stock 5,
// purchase price 2000) ordered 12 units at 2000.
func seedSentPO(t *testing.T, db *gorm.DB) purchaseFixture {
	t.Helper()
	f := purchaseFixture{
		tenantID:   "tenant-a",
		poID:       "po000000-0000-4000-8000-000000000001",
		supplierID: "5a000000-0000-4000-8000-000000000001",
		arrozID:    "10000000-0000-4000-8000-000000000001",
		gaseosaID:  "20000000-0000-4000-8000-000000000001",
	}
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: f.arrozID},
		TenantID:  f.tenantID, Name: "Arroz", Unit: models.UnitKg,
		Stock: 3, MinStock: 1, UnitCost: 2900,
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: f.gaseosaID},
		TenantID:  f.tenantID, Name: "Gaseosa", Price: 2500,
		PurchasePrice: 2000, Stock: 5,
	}).Error)
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel:  models.BaseModel{ID: f.poID},
		TenantID:   f.tenantID,
		SupplierID: f.supplierID,
		Status:     models.PurchaseOrderSent,
		Total:      53000,
		Items: []models.PurchaseOrderItem{
			{
				PurchaseOrderID: f.poID, IngredientID: purStrPtr(f.arrozID),
				NameSnapshot: "Arroz", Quantity: 10, UnitCost: 2900,
			},
			{
				PurchaseOrderID: f.poID, ProductID: purStrPtr(f.gaseosaID),
				NameSnapshot: "Gaseosa", Quantity: 12, UnitCost: 2000,
			},
		},
	}).Error)
	return f
}

// AC-03 — receiving a sent PO enters stock for every item via a
// purchase_receipt kardex movement and flips the PO to recibida.
func TestReceivePurchaseOrder_EntersStockAndCompletes(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	svc := services.NewPurchaseService(db)

	po, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.NoError(t, err)
	assert.Equal(t, models.PurchaseOrderReceived, po.Status)
	require.NotNil(t, po.ReceivedAt, "received_at must be stamped")

	// Insumo: 3 + 10 = 13 (AC-03).
	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 13.0, arroz.Stock, 1e-9, "arroz 3 + 10 = 13")

	// Product: 5 + 12 = 17.
	var gaseosa models.Product
	require.NoError(t, db.First(&gaseosa, "id = ?", f.gaseosaID).Error)
	assert.Equal(t, 17, gaseosa.Stock, "gaseosa 5 + 12 = 17")

	// One purchase_receipt movement per item.
	var movements []models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementPurchaseReceipt).
		Find(&movements).Error)
	assert.Len(t, movements, 2, "one purchase_receipt movement per PO item")
	for _, m := range movements {
		assert.Equal(t, f.poID, *m.ReferenceID, "movement anchored to the PO UUID")
		assert.Equal(t, "purchase_order", m.ReferenceType)
		assert.Greater(t, m.Quantity, float64(0), "a receipt is an incoming (positive) movement")
	}
}

// AC-04 / Art. II — receiving the SAME PO twice is idempotent: stock
// does NOT change on the second receive and no new movements appear.
func TestReceivePurchaseOrder_IdempotentOnReReceive(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	svc := services.NewPurchaseService(db)

	_, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.NoError(t, err)

	// Second receive — must be a safe no-op.
	po2, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.NoError(t, err, "re-receiving an already-received PO must not error")
	assert.Equal(t, models.PurchaseOrderReceived, po2.Status)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 13.0, arroz.Stock, 1e-9, "stock must NOT double on re-receive")

	var gaseosa models.Product
	require.NoError(t, db.First(&gaseosa, "id = ?", f.gaseosaID).Error)
	assert.Equal(t, 17, gaseosa.Stock, "product stock must NOT double on re-receive")

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementPurchaseReceipt).
		Count(&movCount)
	assert.Equal(t, int64(2), movCount, "re-receive must not append duplicate movements")
}

// AC-05 — when an item's unit cost differs from the current cost of
// the insumo / product, the receipt updates it to the PO cost.
func TestReceivePurchaseOrder_UpdatesUnitCost(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db) // arroz cost 2900, gaseosa purchase price 2000

	// Re-write the PO items with new costs.
	require.NoError(t, db.Model(&models.PurchaseOrderItem{}).
		Where("purchase_order_id = ? AND ingredient_id = ?", f.poID, f.arrozID).
		Update("unit_cost", 3100).Error)
	require.NoError(t, db.Model(&models.PurchaseOrderItem{}).
		Where("purchase_order_id = ? AND product_id = ?", f.poID, f.gaseosaID).
		Update("unit_cost", 2200).Error)

	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.NoError(t, err)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 3100.0, arroz.UnitCost, 1e-9, "insumo cost updated to the PO cost (AC-05)")

	var gaseosa models.Product
	require.NoError(t, db.First(&gaseosa, "id = ?", f.gaseosaID).Error)
	assert.InDelta(t, 2200.0, gaseosa.PurchasePrice, 1e-9, "product purchase price updated to the PO cost")
}

// AC-06 — receiving a cancelled PO is rejected and never touches stock.
func TestReceivePurchaseOrder_RejectsCancelledPO(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	require.NoError(t, db.Model(&models.PurchaseOrder{}).
		Where("id = ?", f.poID).Update("status", models.PurchaseOrderCancelled).Error)

	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.Error(t, err, "a cancelled PO cannot be received")

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "a rejected receive must not move stock")
}

// D3 — receiving straight from `borrador` is allowed (compra sin envío).
func TestReceivePurchaseOrder_AllowsReceiveFromDraft(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	require.NoError(t, db.Model(&models.PurchaseOrder{}).
		Where("id = ?", f.poID).Update("status", models.PurchaseOrderDraft).Error)

	svc := services.NewPurchaseService(db)
	po, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.NoError(t, err, "receiving from borrador is allowed (D3)")
	assert.Equal(t, models.PurchaseOrderReceived, po.Status)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 13.0, arroz.Stock, 1e-9)
}

// §9 — a PO with no items cannot be received.
func TestReceivePurchaseOrder_RejectsEmptyPO(t *testing.T) {
	db := setupPurchaseDB(t)
	poID := "po000000-0000-4000-8000-0000000000ee"
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel:  models.BaseModel{ID: poID},
		TenantID:   "tenant-a",
		SupplierID: "5a000000-0000-4000-8000-000000000099",
		Status:     models.PurchaseOrderSent,
	}).Error)

	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder("tenant-a", poID, services.ReceiveContext{})
	require.Error(t, err, "an empty PO cannot be received")
}

// §9 — an item whose insumo was soft-deleted blocks the whole receive
// (atomic: nothing enters stock).
func TestReceivePurchaseOrder_BlocksOnDeletedReference(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	// Soft-delete the arroz insumo referenced by item 1.
	require.NoError(t, db.Delete(&models.Ingredient{}, "id = ?", f.arrozID).Error)

	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.Error(t, err, "a PO referencing a deleted insumo cannot be received")

	// Atomic: the still-valid product line must NOT have moved.
	var gaseosa models.Product
	require.NoError(t, db.First(&gaseosa, "id = ?", f.gaseosaID).Error)
	assert.Equal(t, 5, gaseosa.Stock, "a blocked receive is atomic — nothing enters stock")

	var po models.PurchaseOrder
	require.NoError(t, db.First(&po, "id = ?", f.poID).Error)
	assert.Equal(t, models.PurchaseOrderSent, po.Status, "a blocked receive leaves the PO untouched")
}

// Art. III — another tenant cannot receive this tenant's PO.
func TestReceivePurchaseOrder_TenantIsolation(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	svc := services.NewPurchaseService(db)

	_, err := svc.ReceivePurchaseOrder("tenant-b", f.poID, services.ReceiveContext{})
	require.Error(t, err, "tenant-b must not receive tenant-a's PO")

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "cross-tenant receive must not move stock")
}

// AC-05 — when the PO cost equals the current cost, the receipt still
// succeeds and leaves the cost unchanged (no spurious write).
func TestReceivePurchaseOrder_KeepsCostWhenUnchanged(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db) // arroz cost 2900, item cost 2900 — equal
	svc := services.NewPurchaseService(db)

	_, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.NoError(t, err)

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 2900.0, arroz.UnitCost, 1e-9)
}

// §9 — a PO referencing a soft-deleted PRODUCT also blocks the receive.
func TestReceivePurchaseOrder_BlocksOnDeletedProduct(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	require.NoError(t, db.Delete(&models.Product{}, "id = ?", f.gaseosaID).Error)

	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{})
	require.Error(t, err, "a PO referencing a deleted product cannot be received")
	assert.ErrorIs(t, err, services.ErrPOItemInvalid)

	// Atomic: the still-valid insumo line must NOT have moved.
	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "a blocked receive is atomic")
}

// A receive carrying branch / user metadata stamps it onto each
// kardex movement (offline-first traceability).
func TestReceivePurchaseOrder_StampsBranchAndUser(t *testing.T) {
	db := setupPurchaseDB(t)
	f := seedSentPO(t, db)
	branchID := "b0000000-0000-4000-8000-000000000001"
	userID := "c0000000-0000-4000-8000-000000000001"

	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder(f.tenantID, f.poID, services.ReceiveContext{
		BranchID: &branchID,
		UserID:   &userID,
	})
	require.NoError(t, err)

	var movements []models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementPurchaseReceipt).
		Find(&movements).Error)
	require.Len(t, movements, 2)
	for _, m := range movements {
		require.NotNil(t, m.BranchID)
		assert.Equal(t, branchID, *m.BranchID)
		require.NotNil(t, m.UserID)
		assert.Equal(t, userID, *m.UserID)
	}
}

// A PO that does not exist for the tenant returns ErrPONotFound.
func TestReceivePurchaseOrder_NotFound(t *testing.T) {
	db := setupPurchaseDB(t)
	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder("tenant-a",
		"99999999-9999-4999-8999-999999999999", services.ReceiveContext{})
	require.Error(t, err)
	assert.ErrorIs(t, err, services.ErrPONotFound)
}

// A fractional product quantity is truncated for the integer stock
// column but the kardex movement records the exact figure.
func TestReceivePurchaseOrder_TruncatesFractionalProductQuantity(t *testing.T) {
	db := setupPurchaseDB(t)
	tenantID := "tenant-a"
	prodID := "20000000-0000-4000-8000-0000000000ab"
	poID := "po000000-0000-4000-8000-0000000000ab"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: prodID},
		TenantID:  tenantID, Name: "Caja", Price: 1000, PurchasePrice: 500, Stock: 0,
	}).Error)
	require.NoError(t, db.Create(&models.PurchaseOrder{
		BaseModel:  models.BaseModel{ID: poID},
		TenantID:   tenantID,
		SupplierID: "5a000000-0000-4000-8000-0000000000ab",
		Status:     models.PurchaseOrderSent,
		Items: []models.PurchaseOrderItem{
			{PurchaseOrderID: poID, ProductID: &prodID,
				NameSnapshot: "Caja", Quantity: 3.7, UnitCost: 500},
		},
	}).Error)

	svc := services.NewPurchaseService(db)
	_, err := svc.ReceivePurchaseOrder(tenantID, poID, services.ReceiveContext{})
	require.NoError(t, err)

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", prodID).Error)
	assert.Equal(t, 3, prod.Stock, "product stock is integer — 3.7 truncated to 3")

	var mov models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementPurchaseReceipt).
		First(&mov).Error)
	assert.InDelta(t, 3.7, mov.Quantity, 1e-9, "the kardex keeps the exact figure")
}
