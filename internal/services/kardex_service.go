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
	// Read current stock inside the same transaction.
	var currentStock int
	if err := tx.Model(&models.Product{}).
		Select("stock").
		Where("id = ?", p.ProductID).
		Scan(&currentStock).Error; err != nil {
		return err
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

	after := currentStock + p.Quantity

	// The InventoryMovement columns are float64 so the kardex can hold
	// fractional recipe consumption. Product movements are whole-unit by
	// nature, so widening these ints is a lossless conversion.
	mov := models.InventoryMovement{
		ID:             uuid.NewString(),
		TenantID:       p.TenantID,
		BranchID:       p.BranchID,
		ProductID:      p.ProductID,
		ProductName:    p.ProductName,
		MovementType:   p.MovementType,
		Quantity:       float64(p.Quantity),
		StockBefore:    float64(currentStock),
		StockAfter:     float64(after),
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
