// Spec: specs/001-insumos-recetas/spec.md
package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// parseIngredientExpiry validates an optional expiry date. Empty maps
// to nil (no expiration). Accepts ISO-8601 dates only so the column
// never receives garbage.
func parseIngredientExpiry(raw string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", trimmed)
	if err != nil {
		return nil, fmt.Errorf("expiry_date debe tener formato YYYY-MM-DD")
	}
	return &parsed, nil
}

// ListIngredients returns the tenant's raw-material inventory, paginated.
func ListIngredients(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		query := db.Model(&models.Ingredient{}).Where("tenant_id = ?", tenantID)

		var total int64
		query.Count(&total)

		var ingredients []models.Ingredient
		if err := query.
			Order("name ASC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&ingredients).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener insumos"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(ingredients, total, p))
	}
}

// CreateIngredient registers a new insumo. Idempotent by client-supplied
// UUID (Art. II): re-sending the same id returns the existing row.
func CreateIngredient(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID         string  `json:"id"`
		Name       string  `json:"name"`
		Unit       string  `json:"unit"`
		Stock      float64 `json:"stock"`
		MinStock   float64 `json:"min_stock"`
		UnitCost   float64 `json:"unit_cost"`
		ExpiryDate string  `json:"expiry_date"`
		SupplierID *string `json:"supplier_id"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if strings.TrimSpace(req.Name) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el nombre del insumo es obligatorio"})
			return
		}
		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID v4 válido"})
			return
		}
		if req.Unit != "" && !models.IsValidUnit(req.Unit) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "unidad inválida — use unidad, g, kg, ml o l",
			})
			return
		}
		if req.Stock < 0 || req.MinStock < 0 || req.UnitCost < 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "stock, stock mínimo y costo no pueden ser negativos",
			})
			return
		}
		expiry, err := parseIngredientExpiry(req.ExpiryDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Idempotency (Art. II): a re-sent client UUID returns the
		// existing row instead of failing on the primary key.
		if req.ID != "" {
			var existing models.Ingredient
			if err := db.Where("id = ? AND tenant_id = ?", req.ID, tenantID).
				First(&existing).Error; err == nil {
				c.JSON(http.StatusCreated, gin.H{"data": existing})
				return
			}
		}

		ingredient := models.Ingredient{
			TenantID:   tenantID,
			Name:       strings.TrimSpace(req.Name),
			Unit:       models.NormalizeUnit(req.Unit),
			Stock:      req.Stock,
			MinStock:   req.MinStock,
			UnitCost:   req.UnitCost,
			ExpiryDate: expiry,
			SupplierID: middleware.UUIDPtr(deref(req.SupplierID)),
		}
		if req.ID != "" {
			ingredient.ID = req.ID
		}

		// Create + kardex movement run inside one transaction: a kardex
		// write failure must roll back the ingredient creation instead of
		// leaving a row with no audit trail (Art. VII) and a 201 response
		// that silently hid the error.
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&ingredient).Error; err != nil {
				return err
			}

			// FR-05 — a freshly created insumo with stock inicial > 0 must
			// enter the kardex with an `initial_stock` movement, exactly as
			// products do (CreateProduct). Without it the invariant
			// `stock = Σ movimientos` (Constitución Art. VII) never held for
			// insumos. This runs only on a real insert: the idempotent
			// re-sync path above returns before reaching here, so a re-sent
			// UUID never double-logs. stock_before/stock_after are passed
			// explicitly (0 → stock_inicial) because LogInventoryMovement's
			// self-read targets the products table, not ingredients.
			if ingredient.Stock > 0 {
				zero := float64(0)
				initial := ingredient.Stock
				return services.LogInventoryMovement(tx, services.MovementParams{
					TenantID:            tenantID,
					ProductID:           ingredient.ID,
					ProductName:         ingredient.Name,
					MovementType:        models.MovementInitialStock,
					ReferenceType:       "ingredient",
					StockBeforeOverride: &zero,
					StockAfterOverride:  &initial,
					QuantityOverride:    &initial,
				})
			}
			return nil
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear insumo"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": ingredient})
	}
}

// UpdateIngredient applies partial updates. Stock is intentionally NOT
// editable here — it only ever moves through the kardex (Spec §7,
// Plan §4). Use a kardex movement / restock flow to adjust stock.
func UpdateIngredient(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name       *string  `json:"name"`
		Unit       *string  `json:"unit"`
		MinStock   *float64 `json:"min_stock"`
		UnitCost   *float64 `json:"unit_cost"`
		ExpiryDate *string  `json:"expiry_date"`
		SupplierID *string  `json:"supplier_id"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var ingredient models.Ingredient
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&ingredient).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "insumo no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Name != nil {
			if strings.TrimSpace(*req.Name) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el nombre no puede quedar vacío"})
				return
			}
			updates["name"] = strings.TrimSpace(*req.Name)
		}
		if req.Unit != nil {
			if !models.IsValidUnit(*req.Unit) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "unidad inválida — use unidad, g, kg, ml o l",
				})
				return
			}
			updates["unit"] = *req.Unit
		}
		if req.MinStock != nil {
			if *req.MinStock < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el stock mínimo no puede ser negativo"})
				return
			}
			updates["min_stock"] = *req.MinStock
		}
		if req.UnitCost != nil {
			if *req.UnitCost < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el costo no puede ser negativo"})
				return
			}
			updates["unit_cost"] = *req.UnitCost
		}
		if req.ExpiryDate != nil {
			expiry, err := parseIngredientExpiry(*req.ExpiryDate)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			updates["expiry_date"] = expiry
		}
		if req.SupplierID != nil {
			updates["supplier_id"] = middleware.UUIDPtr(*req.SupplierID)
		}

		if len(updates) > 0 {
			if err := db.Model(&ingredient).Updates(updates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar insumo"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": ingredient})
	}
}

// DeleteIngredient soft-deletes the insumo.
func DeleteIngredient(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		result := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			Delete(&models.Ingredient{})
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar insumo"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "insumo no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "insumo eliminado"})
	}
}

// LowStockIngredients returns insumos at or below their minimum (AC-05).
// Ingredients with MinStock 0 have no threshold and are excluded.
func LowStockIngredients(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var ingredients []models.Ingredient
		if err := db.
			Where("tenant_id = ? AND min_stock > 0 AND stock < min_stock", tenantID).
			Order("name ASC").
			Find(&ingredients).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener insumos bajos"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": ingredients})
	}
}

// deref returns the string value of a *string, or "" when nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
