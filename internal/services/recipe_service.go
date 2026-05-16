// Spec: specs/001-insumos-recetas/spec.md
package services

import (
	"fmt"
	"strings"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RecipeService owns the "recipe explosion": turning the sale of a
// product-receta into ingredient (insumo) consumption. Keeping it out
// of sales.go keeps that handler small (Art. IX) and the logic unit-
// testable in isolation.
type RecipeService struct {
	db *gorm.DB
}

// NewRecipeService builds a RecipeService. The injected *gorm.DB is the
// fallback handle; ExplodeRecipe receives the active transaction so the
// discount is atomic with the sale write.
func NewRecipeService(db *gorm.DB) *RecipeService {
	return &RecipeService{db: db}
}

// ExplodeParams describes one product-receta line being sold.
type ExplodeParams struct {
	TenantID  string // owner tenant — every query filters by it (Art. III)
	SaleUUID  string // the sale this consumption belongs to (idempotency anchor)
	ProductID string // the vendible product that was sold
	Quantity  int    // how many units of the product were sold
	BranchID  *string
	UserID    *string
}

// ExplodeRecipe discounts the ingredients of a product-receta and logs
// one recipe_consumption movement per insumo (FR-03, AC-04).
//
// Contract:
//   - A direct product (IsRecipe == false) or one without a resolvable
//     recipe is a silent no-op — CreateSale calls this on every item.
//   - Idempotent by (SaleUUID, IngredientID): re-syncing the same sale
//     does NOT discount ingredients twice (Art. II, AC idempotency).
//   - Insufficient stock never aborts: the insumo goes negative and the
//     consumption is still logged so the kardex shows the shortfall
//     (D3, AC-07). The venta is never lost.
//
// tx MUST be the active transaction from CreateSale so the discount is
// atomic with the sale row.
func (s *RecipeService) ExplodeRecipe(tx *gorm.DB, p ExplodeParams) error {
	if p.Quantity <= 0 {
		return nil
	}

	// Resolve the product and confirm it is a recipe owned by the
	// tenant. A miss here (direct product, foreign tenant, deleted
	// row) is a deliberate no-op.
	var product models.Product
	if err := tx.Where("id = ? AND tenant_id = ?", p.ProductID, p.TenantID).
		First(&product).Error; err != nil {
		return nil //nolint:nilerr — a non-recipe / not-found product is a no-op by design
	}
	if !product.IsRecipe {
		return nil
	}

	// Find the recipe. Prefer the explicit Product.RecipeID link;
	// fall back to a recipe whose ProductID points back at this
	// product (the two sides of the Plan §3 association).
	recipe, err := s.resolveRecipe(tx, p.TenantID, product)
	if err != nil {
		return err
	}
	if recipe == nil {
		// A recipe-flagged product with no recipe yet: nothing to
		// explode. The venta still goes through (D3).
		return nil
	}

	for _, line := range recipe.Ingredients {
		if line.IngredientID == nil || *line.IngredientID == "" {
			// Legacy line still pointing at a Product (ProductUUID)
			// and never re-oriented to an Ingredient — skip it
			// rather than guessing (Art. IV: no invented behavior).
			continue
		}
		if err := s.consumeIngredient(tx, p, *line.IngredientID, line.Quantity); err != nil {
			return err
		}
	}
	return nil
}

// resolveRecipe loads the recipe for a product-receta with its
// ingredient lines, scoped to the tenant.
func (s *RecipeService) resolveRecipe(tx *gorm.DB, tenantID string, product models.Product) (*models.Recipe, error) {
	var recipe models.Recipe
	q := tx.Preload("Ingredients").Where("tenant_id = ?", tenantID)
	if product.RecipeID != nil && *product.RecipeID != "" {
		q = q.Where("id = ?", *product.RecipeID)
	} else {
		q = q.Where("product_id = ?", product.ID)
	}
	if err := q.First(&recipe).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("resolver receta: %w", err)
	}
	return &recipe, nil
}

// consumeIngredient discounts a single insumo for the whole sale line
// and records the kardex movement. Idempotent per (sale, ingredient).
func (s *RecipeService) consumeIngredient(tx *gorm.DB, p ExplodeParams, ingredientID string, perUnitQty float64) error {
	consumed := perUnitQty * float64(p.Quantity)

	// Idempotency key — one movement per (sale, ingredient). The
	// inventory_movements table already carries a partial unique
	// index on idempotency_key, so a duplicate insert is rejected at
	// the DB layer and we skip the stock discount.
	idemKey := recipeConsumptionKey(p.SaleUUID, ingredientID)

	var existing int64
	if err := tx.Model(&models.InventoryMovement{}).
		Where("idempotency_key = ?", idemKey).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("verificar idempotencia de consumo: %w", err)
	}
	if existing > 0 {
		// Already exploded for this sale — re-sync no-op (Art. II).
		return nil
	}

	// Load the insumo (tenant-scoped). A missing insumo means the
	// recipe references a soft-deleted ingredient; we skip it instead
	// of failing the sale (D3 — venta nunca se pierde).
	var ingredient models.Ingredient
	if err := tx.Where("id = ? AND tenant_id = ?", ingredientID, p.TenantID).
		First(&ingredient).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return fmt.Errorf("cargar insumo: %w", err)
	}

	stockBefore := ingredient.Stock
	stockAfter := stockBefore - consumed

	// Record the movement first so the kardex always reflects the
	// consumption even when stock goes negative (AC-07). The kardex
	// columns are float64, so the fractional figures are stored exactly
	// — nothing is rounded away (Constitución Art. VII: el inventario es
	// exacto).
	mov := models.InventoryMovement{
		ID:             uuid.NewString(),
		TenantID:       p.TenantID,
		BranchID:       p.BranchID,
		ProductID:      ingredient.ID,
		ProductName:    ingredient.Name,
		MovementType:   models.MovementRecipeConsumption,
		Quantity:       -consumed,
		StockBefore:    stockBefore,
		StockAfter:     stockAfter,
		ReferenceID:    strPtr(p.SaleUUID),
		ReferenceType:  "sale",
		UserID:         p.UserID,
		IdempotencyKey: strPtr(idemKey),
		Notes: fmt.Sprintf("recipe_consumption insumo=%s unidad=%s",
			ingredient.Name, ingredient.Unit),
	}
	if err := tx.Create(&mov).Error; err != nil {
		// A racing re-sync could insert the same key between our
		// count and create — treat the duplicate as the idempotent
		// path and skip the discount.
		if strings.Contains(strings.ToLower(err.Error()), "unique") ||
			strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil
		}
		return fmt.Errorf("registrar consumo de receta: %w", err)
	}

	// Discount the insumo. gorm.Expr keeps the float math on the DB
	// side so the stock column never loses precision.
	if err := tx.Model(&models.Ingredient{}).
		Where("id = ? AND tenant_id = ?", ingredient.ID, p.TenantID).
		UpdateColumn("stock", gorm.Expr("stock - ?", consumed)).Error; err != nil {
		return fmt.Errorf("descontar stock de insumo: %w", err)
	}
	return nil
}

// recipeConsumptionKey builds the idempotency key for one ingredient
// consumed by one sale. Stable across re-syncs of the same sale.
func recipeConsumptionKey(saleUUID, ingredientID string) string {
	return "recipe:" + saleUUID + ":" + ingredientID
}

// strPtr returns a pointer to s, or nil when s is empty — so UUID
// columns receive SQL NULL instead of an empty string.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
