package services

import (
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
func LogInventoryMovement(tx *gorm.DB, p MovementParams) error {
	// Read current stock inside the same transaction.
	var currentStock int
	if err := tx.Model(&models.Product{}).
		Select("stock").
		Where("id = ?", p.ProductID).
		Scan(&currentStock).Error; err != nil {
		return err
	}

	after := currentStock + p.Quantity

	mov := models.InventoryMovement{
		ID:             uuid.NewString(),
		TenantID:       p.TenantID,
		BranchID:       p.BranchID,
		ProductID:      p.ProductID,
		ProductName:    p.ProductName,
		MovementType:   p.MovementType,
		Quantity:        p.Quantity,
		StockBefore:    currentStock,
		StockAfter:     after,
		ReferenceID:    p.ReferenceID,
		ReferenceType:  p.ReferenceType,
		UserID:         p.UserID,
		UserName:       p.UserName,
		Notes:          p.Notes,
		IdempotencyKey: p.IdempotencyKey,
	}

	return tx.Create(&mov).Error
}
