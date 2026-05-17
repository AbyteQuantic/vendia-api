// Spec: specs/005-fixes-regresion-360/spec.md
package services

import (
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// SaleInventoryService owns the inventory side effects of a sale: the
// stock decrement of a direct product plus the kardex `sale` movement,
// and the recipe explosion of a product-receta into insumo consumption.
//
// FR-02 — this logic used to live only inside CreateSale, so closing a
// KDS order (CloseOrder) created the sale row but never touched
// inventory. Extracting it to one place (Constitución Art. IX — no
// duplicated logic) lets both CreateSale and CloseOrder apply the exact
// same inventory effects.
type SaleInventoryService struct {
	recipes *RecipeService
}

// NewSaleInventoryService builds the service. The injected *gorm.DB is
// only the fallback handle for the RecipeService; ApplyPostSale always
// receives the active transaction so every write is atomic with the
// sale row.
func NewSaleInventoryService(db *gorm.DB) *SaleInventoryService {
	return &SaleInventoryService{recipes: NewRecipeService(db)}
}

// SaleInventoryLine is one product line of a sale that needs inventory
// applied. Service lines (no product) are NOT passed here — they never
// touch stock.
type SaleInventoryLine struct {
	ProductID string
	Quantity  int
}

// PostSaleParams carries the sale-level context the inventory effects
// need. SaleUUID is the idempotency anchor for the recipe explosion
// (Art. II): re-applying the same sale never discounts insumos twice.
type PostSaleParams struct {
	TenantID string
	SaleUUID string
	BranchID *string
	UserID   *string
	Lines    []SaleInventoryLine
}

// ApplyPostSale applies the inventory side effects of a sale inside the
// given transaction. For each line it resolves the product (tenant- and
// branch-scoped) and either:
//
//   - product-receta → ExplodeRecipe (discount insumos + recipe_consumption
//     movements), idempotent per (SaleUUID, ingredient);
//   - direct product → decrement the product's own stock and log a `sale`
//     movement, exactly as the legacy CreateSale path did (AC-06).
//
// A product that cannot be resolved (deleted, foreign tenant, wrong
// branch) is skipped silently — the sale row is never lost over an
// inventory miss. tx MUST be the active transaction.
func (s *SaleInventoryService) ApplyPostSale(tx *gorm.DB, p PostSaleParams) error {
	for _, line := range p.Lines {
		if line.Quantity <= 0 {
			continue
		}

		// Resolve the product, tenant- and (when set) branch-scoped.
		// The same product UUID can exist in two sedes with independent
		// stock counters, so the branch filter is what keeps Phase-6
		// isolation real.
		var product models.Product
		q := tx.Where("id = ? AND tenant_id = ?", line.ProductID, p.TenantID)
		if p.BranchID != nil && *p.BranchID != "" {
			q = q.Where("branch_id = ?", *p.BranchID)
		}
		if err := q.First(&product).Error; err != nil {
			// Not found / foreign / wrong branch — skip, never abort.
			continue
		}

		if product.IsRecipe {
			// Product-receta: explode into insumo consumption. Idempotent
			// per (SaleUUID, ingredient) so a re-apply is safe.
			if err := s.recipes.ExplodeRecipe(tx, ExplodeParams{
				TenantID:  p.TenantID,
				SaleUUID:  p.SaleUUID,
				ProductID: product.ID,
				Quantity:  line.Quantity,
				BranchID:  p.BranchID,
				UserID:    p.UserID,
			}); err != nil {
				return err
			}
			continue
		}

		// Direct product: decrement its own stock and log the `sale`
		// movement. This mirrors the legacy CreateSale path byte-for-byte
		// (AC-06): same MovementType, same negative Quantity, same
		// ReferenceType "sale", and — deliberately — no ReferenceID, so
		// CreateSale's observable behaviour does not change after the
		// FR-02 refactor.
		//
		// The legacy CreateSale loop ignored the return value of both
		// LogInventoryMovement and the stock UpdateColumn — a kardex
		// hiccup never failed the venta (Constitución Art. I: la venta
		// nunca se pierde). We preserve that contract here: the direct-
		// product side effects are best-effort and never abort the
		// transaction. Recipe explosion, by contrast, DOES propagate
		// errors — that path was always transactional.
		if product.Stock > 0 {
			_ = LogInventoryMovement(tx, MovementParams{
				TenantID:      p.TenantID,
				BranchID:      p.BranchID,
				ProductID:     product.ID,
				ProductName:   product.Name,
				MovementType:  models.MovementSale,
				Quantity:      -line.Quantity,
				ReferenceType: "sale",
				UserID:        p.UserID,
			})
			_ = tx.Model(&product).
				UpdateColumn("stock", gorm.Expr("stock - ?", line.Quantity)).Error
		}
	}
	return nil
}
