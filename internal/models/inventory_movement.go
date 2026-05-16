package models

import "time"

type MovementType string

const (
	MovementSale         MovementType = "sale"
	MovementInvoiceScan  MovementType = "invoice_scan"
	MovementVoiceIngest  MovementType = "voice_ingest"
	MovementOrderCancel  MovementType = "order_cancel"
	MovementSaleCancel   MovementType = "sale_cancel"
	MovementTableTab     MovementType = "table_tab"
	MovementTabClose     MovementType = "tab_close"
	MovementManualAdjust MovementType = "manual_adjust"
	MovementInitialStock MovementType = "initial_stock"
	// MovementRecipeConsumption (Feature 001) — an ingredient consumed
	// because a product-receta was sold. One movement per ingredient
	// per recipe explosion (FR-03).
	MovementRecipeConsumption MovementType = "recipe_consumption"
	// MovementPurchaseReceipt (Feature 002) — stock entering because a
	// purchase order was received. One movement per PO item; the kardex
	// records the entry exactly once, anchored by the PO UUID so a
	// re-receive never double-counts (Spec §7, D4).
	MovementPurchaseReceipt MovementType = "purchase_receipt"
	// MovementWorkOrderConsumption (Feature 003) — an insumo/producto
	// consumed because a furniture work order was marked `terminada`.
	// One movement per material item, anchored by the work order UUID
	// so a re-complete (or an offline re-sync) never double-counts
	// (Spec §7, FR-05, AC-04).
	MovementWorkOrderConsumption MovementType = "work_order_consumption"
)

type InventoryMovement struct {
	ID           string       `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID     string       `gorm:"type:uuid;not null;index" json:"tenant_id"`
	BranchID     *string      `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	ProductID    string       `gorm:"type:uuid;not null;index:idx_inv_mov_product_created,priority:1" json:"product_id"`
	ProductName  string       `gorm:"type:varchar(256)" json:"product_name"`
	MovementType MovementType `gorm:"type:varchar(32);not null;index" json:"movement_type"`
	// Quantity, StockBefore and StockAfter are float64 so the kardex can
	// record fractional consumption exactly (e.g. 0.3 kg of an insumo by
	// recipe explosion). GORM maps them to a Postgres numeric column;
	// AutoMigrate widens the legacy integer columns via a safe additive
	// ALTER COLUMN ... TYPE numeric cast (Constitución Art. X). Product
	// stock itself stays integer-valued — only the movement trail widens.
	Quantity       float64   `gorm:"not null" json:"quantity"`
	StockBefore    float64   `gorm:"not null" json:"stock_before"`
	StockAfter     float64   `gorm:"not null" json:"stock_after"`
	ReferenceID    *string   `gorm:"type:uuid" json:"reference_id,omitempty"`
	ReferenceType  string    `gorm:"type:varchar(32)" json:"reference_type,omitempty"`
	UserID         *string   `gorm:"type:uuid" json:"user_id,omitempty"`
	UserName       string    `gorm:"type:varchar(128)" json:"user_name,omitempty"`
	Notes          string    `gorm:"type:text" json:"notes,omitempty"`
	IdempotencyKey *string   `gorm:"type:varchar(128);uniqueIndex:idx_inv_mov_idempotency,where:idempotency_key IS NOT NULL" json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `gorm:"index:idx_inv_mov_product_created,priority:2,sort:desc" json:"created_at"`
}
