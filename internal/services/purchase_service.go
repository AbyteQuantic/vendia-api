// Spec: specs/002-ordenes-compra/spec.md
package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PurchaseService owns "receiving a purchase order": entering stock for
// every item via the kardex. Keeping it out of the handler keeps that
// file small (Art. IX) and the critical idempotency logic unit-testable
// in isolation.
type PurchaseService struct {
	db *gorm.DB
}

// NewPurchaseService builds a PurchaseService. The injected *gorm.DB is
// the handle the receive transaction runs on.
func NewPurchaseService(db *gorm.DB) *PurchaseService {
	return &PurchaseService{db: db}
}

// ReceiveContext carries the optional actor / branch metadata stamped
// onto each kardex movement. Both fields tolerate being empty — a
// legacy token may carry neither (Art. X).
type ReceiveContext struct {
	BranchID *string
	UserID   *string
}

// Sentinel errors so the handler can map them to the right HTTP code.
var (
	// ErrPONotFound — the PO does not exist for this tenant.
	ErrPONotFound = errors.New("orden de compra no encontrada")
	// ErrPONotReceivable — the PO is in a status that cannot be received
	// (cancelada, or any state outside borrador/enviada).
	ErrPONotReceivable = errors.New("la orden de compra no se puede recibir en su estado actual")
	// ErrPOEmpty — a PO without items cannot be received (§9).
	ErrPOEmpty = errors.New("la orden de compra no tiene ítems para recibir")
	// ErrPOItemInvalid — an item references a deleted insumo/producto
	// and blocks the whole receive (§9 — atomic).
	ErrPOItemInvalid = errors.New("un ítem de la orden referencia un insumo o producto inválido")
)

// purchaseReceiptKey builds the idempotency key for one PO item's
// receipt. Anchored by the PO UUID so a re-receive (or an offline
// re-sync) is recognised and skipped (Spec §7, D4, AC-04).
func purchaseReceiptKey(poID, itemID string) string {
	return "po_receipt:" + poID + ":" + itemID
}

// ReceivePurchaseOrder enters stock for every item of the PO and flips
// it to `recibida` (FR-05, AC-03).
//
// Contract:
//   - Receivable only from `borrador` (D3) or `enviada`; a `cancelada`
//     or already-`recibida` PO outside that set is rejected — EXCEPT a
//     PO already `recibida` is treated as an idempotent success so a
//     re-sync never errors (Art. II, AC-04).
//   - Idempotent by (PO UUID, item UUID): re-receiving never
//     double-counts stock — each purchase_receipt movement carries a
//     unique idempotency key.
//   - Stock moves ONLY through a purchase_receipt kardex movement,
//     never a direct write (Spec §7, D4, Art. VII).
//   - When an item's unit cost differs from the current cost of the
//     insumo/producto, the cost is updated to the PO cost (FR-06, AC-05).
//   - Atomic: a single bad item (deleted reference) aborts the whole
//     receive — nothing enters stock (§9).
func (s *PurchaseService) ReceivePurchaseOrder(tenantID, poID string, rc ReceiveContext) (*models.PurchaseOrder, error) {
	var result models.PurchaseOrder

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var po models.PurchaseOrder
		if err := tx.Preload("Items").
			Where("id = ? AND tenant_id = ?", poID, tenantID).
			First(&po).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPONotFound
			}
			return fmt.Errorf("cargar orden de compra: %w", err)
		}

		// An already-received PO is an idempotent success: re-receiving
		// is a no-op, never an error (Art. II — re-sync safe).
		if po.Status == models.PurchaseOrderReceived {
			result = po
			return nil
		}
		// Only borrador / enviada can be received (D3, §7).
		if !po.CanTransitionTo(models.PurchaseOrderReceived) {
			return ErrPONotReceivable
		}
		if len(po.Items) == 0 {
			return ErrPOEmpty
		}

		for _, item := range po.Items {
			if err := s.receiveItem(tx, tenantID, po.ID, item, rc); err != nil {
				return err
			}
		}

		// Flip the PO to recibida and stamp the receipt time.
		now := time.Now()
		if err := tx.Model(&models.PurchaseOrder{}).
			Where("id = ? AND tenant_id = ?", po.ID, tenantID).
			Updates(map[string]any{
				"status":      models.PurchaseOrderReceived,
				"received_at": now,
			}).Error; err != nil {
			return fmt.Errorf("marcar orden como recibida: %w", err)
		}

		po.Status = models.PurchaseOrderReceived
		po.ReceivedAt = &now
		result = po
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// receiveItem enters stock for one PO item — either an insumo or a
// product (D1). Idempotent by (PO, item): the purchase_receipt
// movement carries a unique idempotency key, so a duplicate insert is
// caught and the stock update is skipped.
func (s *PurchaseService) receiveItem(tx *gorm.DB, tenantID, poID string, item models.PurchaseOrderItem, rc ReceiveContext) error {
	if !item.IsValidReference() {
		return ErrPOItemInvalid
	}

	// Idempotency: if a receipt movement for this item already exists,
	// this PO was received before — skip the whole line (Art. II).
	idemKey := purchaseReceiptKey(poID, item.ID)
	var existing int64
	if err := tx.Model(&models.InventoryMovement{}).
		Where("idempotency_key = ?", idemKey).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("verificar idempotencia de recepción: %w", err)
	}
	if existing > 0 {
		return nil
	}

	if hasIngredientRef(item) {
		return s.receiveIngredientItem(tx, tenantID, poID, item, idemKey, rc)
	}
	return s.receiveProductItem(tx, tenantID, poID, item, idemKey, rc)
}

// hasIngredientRef reports whether the item targets an insumo.
func hasIngredientRef(item models.PurchaseOrderItem) bool {
	return item.IngredientID != nil && *item.IngredientID != ""
}

// receiveIngredientItem enters stock into an Ingredient and records the
// kardex movement. The insumo cost is bumped to the PO cost when it
// differs (FR-06, AC-05).
func (s *PurchaseService) receiveIngredientItem(tx *gorm.DB, tenantID, poID string, item models.PurchaseOrderItem, idemKey string, rc ReceiveContext) error {
	var ing models.Ingredient
	if err := tx.Where("id = ? AND tenant_id = ?", *item.IngredientID, tenantID).
		First(&ing).Error; err != nil {
		// A missing insumo (soft-deleted) blocks the receive — §9.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPOItemInvalid
		}
		return fmt.Errorf("cargar insumo: %w", err)
	}

	stockBefore := ing.Stock
	stockAfter := stockBefore + item.Quantity

	if err := s.logReceiptMovement(tx, tenantID, poID, idemKey, receiptMovement{
		referenceName: ing.Name,
		targetID:      ing.ID,
		quantity:      item.Quantity,
		stockBefore:   stockBefore,
		stockAfter:    stockAfter,
		notes:         fmt.Sprintf("purchase_receipt insumo=%s", ing.Name),
	}, rc); err != nil {
		// A racing duplicate means this item was already received —
		// skip the stock update (Art. II, AC-04).
		if errors.Is(err, ErrDuplicateMovement) {
			return nil
		}
		return err
	}

	// Stock moves ONLY through the kardex movement above; the update
	// here just reflects the recorded after-value (D4, Art. VII).
	if err := tx.Model(&models.Ingredient{}).
		Where("id = ? AND tenant_id = ?", ing.ID, tenantID).
		UpdateColumn("stock", gorm.Expr("stock + ?", item.Quantity)).Error; err != nil {
		return fmt.Errorf("entrar stock de insumo: %w", err)
	}

	// FR-06 / AC-05 — update the insumo cost to the PO cost on change.
	if item.UnitCost != ing.UnitCost {
		if err := tx.Model(&models.Ingredient{}).
			Where("id = ? AND tenant_id = ?", ing.ID, tenantID).
			UpdateColumn("unit_cost", item.UnitCost).Error; err != nil {
			return fmt.Errorf("actualizar costo de insumo: %w", err)
		}
	}
	return nil
}

// receiveProductItem enters stock into a Product and records the kardex
// movement. The product purchase price is bumped to the PO cost when it
// differs (FR-06, AC-05).
func (s *PurchaseService) receiveProductItem(tx *gorm.DB, tenantID, poID string, item models.PurchaseOrderItem, idemKey string, rc ReceiveContext) error {
	var prod models.Product
	if err := tx.Where("id = ? AND tenant_id = ?", *item.ProductID, tenantID).
		First(&prod).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPOItemInvalid
		}
		return fmt.Errorf("cargar producto: %w", err)
	}

	// Product stock is integer-valued; a fractional PO quantity is
	// truncated for the stock column but the movement records the
	// exact figure on the float kardex.
	qtyInt := int(item.Quantity)
	stockBefore := float64(prod.Stock)
	stockAfter := stockBefore + item.Quantity

	if err := s.logReceiptMovement(tx, tenantID, poID, idemKey, receiptMovement{
		referenceName: prod.Name,
		targetID:      prod.ID,
		quantity:      item.Quantity,
		stockBefore:   stockBefore,
		stockAfter:    stockAfter,
		notes:         fmt.Sprintf("purchase_receipt producto=%s", prod.Name),
	}, rc); err != nil {
		// A racing duplicate means this item was already received —
		// skip the stock update (Art. II, AC-04).
		if errors.Is(err, ErrDuplicateMovement) {
			return nil
		}
		return err
	}

	if err := tx.Model(&models.Product{}).
		Where("id = ? AND tenant_id = ?", prod.ID, tenantID).
		UpdateColumn("stock", gorm.Expr("stock + ?", qtyInt)).Error; err != nil {
		return fmt.Errorf("entrar stock de producto: %w", err)
	}

	if item.UnitCost != prod.PurchasePrice {
		if err := tx.Model(&models.Product{}).
			Where("id = ? AND tenant_id = ?", prod.ID, tenantID).
			UpdateColumn("purchase_price", item.UnitCost).Error; err != nil {
			return fmt.Errorf("actualizar costo de producto: %w", err)
		}
	}
	return nil
}

// receiptMovement bundles the figures of a single purchase_receipt
// kardex entry so logReceiptMovement keeps a short signature.
type receiptMovement struct {
	referenceName string
	targetID      string
	quantity      float64
	stockBefore   float64
	stockAfter    float64
	notes         string
}

// logReceiptMovement writes one purchase_receipt InventoryMovement,
// anchored by the PO UUID. The unique idempotency key makes a re-receive
// a no-op at the DB layer (Spec §7, D4, AC-04).
func (s *PurchaseService) logReceiptMovement(tx *gorm.DB, tenantID, poID, idemKey string, rm receiptMovement, rc ReceiveContext) error {
	mov := models.InventoryMovement{
		ID:             uuid.NewString(),
		TenantID:       tenantID,
		BranchID:       rc.BranchID,
		ProductID:      rm.targetID,
		ProductName:    rm.referenceName,
		MovementType:   models.MovementPurchaseReceipt,
		Quantity:       rm.quantity,
		StockBefore:    rm.stockBefore,
		StockAfter:     rm.stockAfter,
		ReferenceID:    strPtr(poID),
		ReferenceType:  "purchase_order",
		UserID:         rc.UserID,
		IdempotencyKey: strPtr(idemKey),
		Notes:          rm.notes,
	}
	if err := tx.Create(&mov).Error; err != nil {
		// A racing re-receive could insert the same key between our
		// count and create — treat the duplicate as the idempotent
		// path and skip the stock update by surfacing it as a no-op.
		if isDuplicateKeyErr(err) {
			return ErrDuplicateMovement
		}
		return fmt.Errorf("registrar recepción en kardex: %w", err)
	}
	return nil
}

// isDuplicateKeyErr reports whether err is a unique-constraint
// violation — works across the Postgres prod driver and the sqlite
// test driver.
func isDuplicateKeyErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}
