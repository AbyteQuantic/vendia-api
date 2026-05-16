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
)

type InventoryMovement struct {
	ID             string       `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID       string       `gorm:"type:uuid;not null;index" json:"tenant_id"`
	BranchID       *string      `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	ProductID      string       `gorm:"type:uuid;not null;index:idx_inv_mov_product_created,priority:1" json:"product_id"`
	ProductName    string       `gorm:"type:varchar(256)" json:"product_name"`
	MovementType   MovementType `gorm:"type:varchar(32);not null;index" json:"movement_type"`
	Quantity       int          `gorm:"not null" json:"quantity"`
	StockBefore    int          `gorm:"not null" json:"stock_before"`
	StockAfter     int          `gorm:"not null" json:"stock_after"`
	ReferenceID    *string      `gorm:"type:uuid" json:"reference_id,omitempty"`
	ReferenceType  string       `gorm:"type:varchar(32)" json:"reference_type,omitempty"`
	UserID         *string      `gorm:"type:uuid" json:"user_id,omitempty"`
	UserName       string       `gorm:"type:varchar(128)" json:"user_name,omitempty"`
	Notes          string       `gorm:"type:text" json:"notes,omitempty"`
	IdempotencyKey *string      `gorm:"type:varchar(128);uniqueIndex:idx_inv_mov_idempotency,where:idempotency_key IS NOT NULL" json:"idempotency_key,omitempty"`
	CreatedAt      time.Time    `gorm:"index:idx_inv_mov_product_created,priority:2,sort:desc" json:"created_at"`
}
