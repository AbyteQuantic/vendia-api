package services

import (
	"fmt"
	"strings"
	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MovementParams holds everything needed to log an inventory movement.
type MovementParams struct {
	TenantID      string
	BranchID      *string
	ProductID     string
	ProductName   string
	MovementType  models.MovementType
	Quantity      int // signed: +incoming, -outgoing
	ReferenceID   *string
	ReferenceType string
	UserID        *string
	UserName      string
	Notes         string
	// IdempotencyKey prevents double-counting when the same event fires
	// twice (e.g. duplicate invoice scan). Leave nil when not applicable.
	IdempotencyKey *string
	// StockBeforeOverride / StockAfterOverride let the caller dictate the
	// before/after snapshot instead of having LogInventoryMovement read
	// the current stock off the products row. Needed by CreateProduct
	// (FR-03): the product row already carries stock_inicial by the time
	// the movement is logged, so a self-read would record
	// stock_before=stock_inicial / stock_after=2×stock_inicial. With the
	// override the movement correctly records 0 → stock_inicial. Both
	// must be set together; leaving them nil keeps the legacy self-read
	// behaviour for every existing caller.
	StockBeforeOverride *float64
	StockAfterOverride  *float64
	// QuantityOverride records a fractional movement quantity exactly.
	// MovementParams.Quantity is int because product stock is whole-unit,
	// but an insumo can move in fractional units (e.g. 0.5 kg). When set,
	// QuantityOverride is stored verbatim instead of float64(Quantity) so
	// `stock = Σ movimientos` holds for insumos (Constitución Art. VII).
	QuantityOverride *float64
}

// LogInventoryMovement creates an InventoryMovement record inside the
// provided transaction. It reads the current stock atomically and
// records the before/after snapshot. The caller is responsible for
// actually modifying the stock on the Product row — this function
// only records the movement.
//
// When UserName is empty but UserID is set, the function resolves the
// name from the users table so the kardex always shows who did it.
func LogInventoryMovement(tx *gorm.DB, p MovementParams) error {
	// Read current stock inside the same transaction — unless the caller
	// supplied an explicit before/after snapshot (FR-03). A self-read is
	// wrong whenever the products row was already mutated before logging.
	var currentStock int
	if p.StockBeforeOverride == nil {
		if err := tx.Model(&models.Product{}).
			Select("stock").
			Where("id = ?", p.ProductID).
			Scan(&currentStock).Error; err != nil {
			return err
		}
	}

	// Auto-resolve user name when the caller only passed UserID.
	userName := p.UserName
	if userName == "" && p.UserID != nil && *p.UserID != "" {
		var u struct{ Name string }
		if err := tx.Table("users").Select("name").
			Where("id = ?", *p.UserID).Scan(&u).Error; err == nil && u.Name != "" {
			userName = u.Name
		}
		// Fallback: check employees table (legacy single-tenant tokens
		// may carry an employee UUID instead of a user UUID).
		if userName == "" {
			var e struct{ Name string }
			if err := tx.Table("employees").Select("name").
				Where("id = ?", *p.UserID).Scan(&e).Error; err == nil && e.Name != "" {
				userName = e.Name
			}
		}
	}

	// Resolve the before/after snapshot. Default: self-read + arithmetic.
	// Override: the caller knows the true snapshot (FR-03 — CreateProduct
	// always starts at 0 and ends at stock_inicial).
	stockBefore := float64(currentStock)
	stockAfter := float64(currentStock + p.Quantity)
	if p.StockBeforeOverride != nil && p.StockAfterOverride != nil {
		stockBefore = *p.StockBeforeOverride
		stockAfter = *p.StockAfterOverride
	}

	// The InventoryMovement columns are float64 so the kardex can hold
	// fractional recipe consumption. Product movements are whole-unit by
	// nature, so widening these ints is a lossless conversion; an insumo
	// movement can pass QuantityOverride to record a fractional amount.
	quantity := float64(p.Quantity)
	if p.QuantityOverride != nil {
		quantity = *p.QuantityOverride
	}
	mov := models.InventoryMovement{
		ID:             uuid.NewString(),
		TenantID:       p.TenantID,
		BranchID:       p.BranchID,
		ProductID:      p.ProductID,
		ProductName:    p.ProductName,
		MovementType:   p.MovementType,
		Quantity:       quantity,
		StockBefore:    stockBefore,
		StockAfter:     stockAfter,
		ReferenceID:    p.ReferenceID,
		ReferenceType:  p.ReferenceType,
		UserID:         p.UserID,
		UserName:       userName,
		Notes:          p.Notes,
		IdempotencyKey: p.IdempotencyKey,
	}

	err := tx.Create(&mov).Error
	// Idempotency: if the key already exists, skip the duplicate and
	// return ErrDuplicateMovement so the caller can also skip the
	// stock update.
	if err != nil && p.IdempotencyKey != nil && strings.Contains(err.Error(), "duplicate key") {
		return ErrDuplicateMovement
	}
	return err
}

// ErrDuplicateMovement signals that a movement with the same
// idempotency key already exists. Callers should skip the
// corresponding stock update.
var ErrDuplicateMovement = fmt.Errorf("movimiento duplicado ignorado")
