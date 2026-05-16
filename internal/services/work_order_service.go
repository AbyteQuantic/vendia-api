// Spec: specs/003-trabajos-muebles/spec.md
package services

import (
	"errors"
	"fmt"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WorkOrderService owns "completing a work order": discounting stock for
// every material item via the kardex when the job is marked `terminada`.
// Keeping it out of the handler keeps that file small (Art. IX) and the
// critical idempotency logic unit-testable in isolation (mirrors
// PurchaseService of Feature 002).
type WorkOrderService struct {
	db *gorm.DB
}

// NewWorkOrderService builds a WorkOrderService. The injected *gorm.DB
// is the handle the completion transaction runs on.
func NewWorkOrderService(db *gorm.DB) *WorkOrderService {
	return &WorkOrderService{db: db}
}

// WorkOrderContext carries the optional actor / branch metadata stamped
// onto each kardex movement. Both fields tolerate being empty — a
// legacy token may carry neither (Art. X).
type WorkOrderContext struct {
	BranchID *string
	UserID   *string
}

// Sentinel errors so the handler can map them to the right HTTP code.
var (
	// ErrWONotFound — the work order does not exist for this tenant.
	ErrWONotFound = errors.New("trabajo no encontrado")
	// ErrWONotCompletable — the work order is in a status that cannot
	// transition to `terminada` (FR-03, AC-05).
	ErrWONotCompletable = errors.New("el trabajo no se puede terminar en su estado actual")
	// ErrWOItemInvalid — a material item references a deleted
	// insumo/producto and blocks the whole completion (§9 — atomic).
	ErrWOItemInvalid = errors.New("un ítem de material referencia un insumo o producto inválido")
)

// workOrderConsumptionKey builds the idempotency key for one material
// item's consumption. Anchored by the work order UUID so a re-complete
// (or an offline re-sync) is recognised and skipped (Spec §7, FR-05,
// AC-04).
func workOrderConsumptionKey(woID, itemID string) string {
	return "wo_consumption:" + woID + ":" + itemID
}

// CompleteWorkOrder marks a work order `terminada` and discounts stock
// for every material item via the kardex (FR-05, AC-03/04).
//
// Contract:
//   - Completable only from `en_proceso`; any other non-completed status
//     is rejected — EXCEPT a work order already `terminada` is treated as
//     an idempotent success so a re-sync never errors (Art. II, AC-04).
//   - Idempotent by (work order UUID, item UUID): re-completing never
//     double-discounts stock — each work_order_consumption movement
//     carries a unique idempotency key.
//   - Stock moves ONLY through a work_order_consumption kardex movement,
//     never a direct write (Spec §7, Art. VII).
//   - Insufficient stock does NOT block completion — the material was
//     already used; the insumo simply goes negative (§9).
//   - Atomic: a single bad material item (deleted reference) aborts the
//     whole completion — nothing moves (§9).
func (s *WorkOrderService) CompleteWorkOrder(tenantID, woID string, wc WorkOrderContext) (*models.WorkOrder, error) {
	var result models.WorkOrder

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var wo models.WorkOrder
		if err := tx.Preload("Items").
			Where("id = ? AND tenant_id = ?", woID, tenantID).
			First(&wo).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWONotFound
			}
			return fmt.Errorf("cargar trabajo: %w", err)
		}

		// An already-completed work order is an idempotent success:
		// re-completing is a no-op, never an error (Art. II, AC-04).
		if wo.Status == models.WorkOrderCompleted {
			result = wo
			return nil
		}
		// Only en_proceso can transition to terminada (FR-03, AC-05).
		if !wo.CanTransitionTo(models.WorkOrderCompleted) {
			return ErrWONotCompletable
		}

		for _, item := range wo.Items {
			if item.Kind != models.WorkOrderItemMaterial {
				continue // labour lines move no stock (FR-05).
			}
			if err := s.consumeItem(tx, tenantID, wo.ID, item, wc); err != nil {
				return err
			}
		}

		// Flip the work order to terminada and stamp the completion time.
		now := time.Now()
		if err := tx.Model(&models.WorkOrder{}).
			Where("id = ? AND tenant_id = ?", wo.ID, tenantID).
			Updates(map[string]any{
				"status":       models.WorkOrderCompleted,
				"completed_at": now,
			}).Error; err != nil {
			return fmt.Errorf("marcar trabajo como terminado: %w", err)
		}

		wo.Status = models.WorkOrderCompleted
		wo.CompletedAt = &now
		result = wo
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// consumeItem discounts stock for one material item — either an insumo
// or a product. Idempotent by (work order, item): the
// work_order_consumption movement carries a unique idempotency key, so
// a duplicate insert is caught and the stock update is skipped.
func (s *WorkOrderService) consumeItem(tx *gorm.DB, tenantID, woID string, item models.WorkOrderItem, wc WorkOrderContext) error {
	if !item.IsValidReference() {
		return ErrWOItemInvalid
	}

	// Idempotency: if a consumption movement for this item already
	// exists, this work order was completed before — skip the line
	// (Art. II, AC-04).
	idemKey := workOrderConsumptionKey(woID, item.ID)
	var existing int64
	if err := tx.Model(&models.InventoryMovement{}).
		Where("idempotency_key = ?", idemKey).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("verificar idempotencia de consumo: %w", err)
	}
	if existing > 0 {
		return nil
	}

	if hasWorkOrderIngredientRef(item) {
		return s.consumeIngredientItem(tx, tenantID, woID, item, idemKey, wc)
	}
	return s.consumeProductItem(tx, tenantID, woID, item, idemKey, wc)
}

// hasWorkOrderIngredientRef reports whether the item targets an insumo.
func hasWorkOrderIngredientRef(item models.WorkOrderItem) bool {
	return item.IngredientID != nil && *item.IngredientID != ""
}

// consumeIngredientItem discounts stock from an Ingredient and records
// the kardex movement. Insufficient stock is allowed — the insumo goes
// negative (§9).
func (s *WorkOrderService) consumeIngredientItem(tx *gorm.DB, tenantID, woID string, item models.WorkOrderItem, idemKey string, wc WorkOrderContext) error {
	var ing models.Ingredient
	if err := tx.Where("id = ? AND tenant_id = ?", *item.IngredientID, tenantID).
		First(&ing).Error; err != nil {
		// A missing insumo (soft-deleted) blocks the completion — §9.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWOItemInvalid
		}
		return fmt.Errorf("cargar insumo: %w", err)
	}

	stockBefore := ing.Stock
	stockAfter := stockBefore - item.Quantity

	if err := s.logConsumptionMovement(tx, tenantID, woID, idemKey, consumptionMovement{
		referenceName: ing.Name,
		targetID:      ing.ID,
		quantity:      item.Quantity,
		stockBefore:   stockBefore,
		stockAfter:    stockAfter,
		notes:         fmt.Sprintf("work_order_consumption insumo=%s", ing.Name),
	}, wc); err != nil {
		// A racing duplicate means this item was already consumed —
		// skip the stock update (Art. II, AC-04).
		if errors.Is(err, ErrDuplicateMovement) {
			return nil
		}
		return err
	}

	// Stock moves ONLY through the kardex movement above; the update
	// here just reflects the recorded after-value (Art. VII).
	if err := tx.Model(&models.Ingredient{}).
		Where("id = ? AND tenant_id = ?", ing.ID, tenantID).
		UpdateColumn("stock", gorm.Expr("stock - ?", item.Quantity)).Error; err != nil {
		return fmt.Errorf("descontar stock de insumo: %w", err)
	}
	return nil
}

// consumeProductItem discounts stock from a Product and records the
// kardex movement. Insufficient stock is allowed — the product goes
// negative (§9).
func (s *WorkOrderService) consumeProductItem(tx *gorm.DB, tenantID, woID string, item models.WorkOrderItem, idemKey string, wc WorkOrderContext) error {
	var prod models.Product
	if err := tx.Where("id = ? AND tenant_id = ?", *item.ProductID, tenantID).
		First(&prod).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWOItemInvalid
		}
		return fmt.Errorf("cargar producto: %w", err)
	}

	// Product stock is integer-valued; a fractional work order quantity
	// is truncated for the stock column but the movement records the
	// exact figure on the float kardex.
	qtyInt := int(item.Quantity)
	stockBefore := float64(prod.Stock)
	stockAfter := stockBefore - item.Quantity

	if err := s.logConsumptionMovement(tx, tenantID, woID, idemKey, consumptionMovement{
		referenceName: prod.Name,
		targetID:      prod.ID,
		quantity:      item.Quantity,
		stockBefore:   stockBefore,
		stockAfter:    stockAfter,
		notes:         fmt.Sprintf("work_order_consumption producto=%s", prod.Name),
	}, wc); err != nil {
		if errors.Is(err, ErrDuplicateMovement) {
			return nil
		}
		return err
	}

	if err := tx.Model(&models.Product{}).
		Where("id = ? AND tenant_id = ?", prod.ID, tenantID).
		UpdateColumn("stock", gorm.Expr("stock - ?", qtyInt)).Error; err != nil {
		return fmt.Errorf("descontar stock de producto: %w", err)
	}
	return nil
}

// consumptionMovement bundles the figures of a single
// work_order_consumption kardex entry so logConsumptionMovement keeps a
// short signature.
type consumptionMovement struct {
	referenceName string
	targetID      string
	quantity      float64
	stockBefore   float64
	stockAfter    float64
	notes         string
}

// logConsumptionMovement writes one work_order_consumption
// InventoryMovement, anchored by the work order UUID. The unique
// idempotency key makes a re-complete a no-op at the DB layer (Spec §7,
// FR-05, AC-04). Quantity is recorded NEGATIVE — a consumption is an
// outgoing movement.
func (s *WorkOrderService) logConsumptionMovement(tx *gorm.DB, tenantID, woID, idemKey string, cm consumptionMovement, wc WorkOrderContext) error {
	woRef := woID
	keyRef := idemKey
	mov := models.InventoryMovement{
		ID:             uuid.NewString(),
		TenantID:       tenantID,
		BranchID:       wc.BranchID,
		ProductID:      cm.targetID,
		ProductName:    cm.referenceName,
		MovementType:   models.MovementWorkOrderConsumption,
		Quantity:       -cm.quantity,
		StockBefore:    cm.stockBefore,
		StockAfter:     cm.stockAfter,
		ReferenceID:    &woRef,
		ReferenceType:  "work_order",
		UserID:         wc.UserID,
		IdempotencyKey: &keyRef,
		Notes:          cm.notes,
	}
	if err := tx.Create(&mov).Error; err != nil {
		// A racing re-complete could insert the same key between our
		// count and create — treat the duplicate as the idempotent
		// path and skip the stock update by surfacing it as a no-op.
		if isDuplicateKeyErr(err) {
			return ErrDuplicateMovement
		}
		return fmt.Errorf("registrar consumo en kardex: %w", err)
	}
	return nil
}
