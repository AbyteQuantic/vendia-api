package handlers

import (
	"math"
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListRecipes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var recipes []models.Recipe
		if err := db.Preload("Ingredients").
			Where("tenant_id = ?", tenantID).
			Order("product_name ASC").
			Find(&recipes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener recetas"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": recipes})
	}
}

func CreateRecipe(db *gorm.DB) gin.HandlerFunc {
	type IngredientInput struct {
		ProductUUID string  `json:"product_uuid" binding:"required"`
		ProductName string  `json:"product_name" binding:"required"`
		Quantity    float64 `json:"quantity"      binding:"required,gt=0"`
		UnitCost    float64 `json:"unit_cost"     binding:"required,gt=0"`
		Emoji       string  `json:"emoji"`
	}

	type Request struct {
		ID          string            `json:"id"`
		ProductName string            `json:"product_name" binding:"required"`
		Category    string            `json:"category"`
		SalePrice   float64           `json:"sale_price"   binding:"required,gt=0"`
		Emoji       string            `json:"emoji"`
		PhotoURL    string            `json:"photo_url"`
		Ingredients []IngredientInput `json:"ingredients"  binding:"required,min=1"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID v4 válido"})
			return
		}

		var ingredients []models.RecipeIngredient
		for _, ing := range req.Ingredients {
			ingredients = append(ingredients, models.RecipeIngredient{
				ProductUUID: ing.ProductUUID,
				ProductName: ing.ProductName,
				Quantity:    ing.Quantity,
				UnitCost:    ing.UnitCost,
				Emoji:       ing.Emoji,
			})
		}

		recipe := models.Recipe{
			TenantID:    tenantID,
			ProductName: req.ProductName,
			Category:    req.Category,
			SalePrice:   req.SalePrice,
			Emoji:       req.Emoji,
			PhotoURL:    req.PhotoURL,
			Ingredients: ingredients,
		}
		if req.ID != "" {
			recipe.ID = req.ID
		}

		if err := db.Create(&recipe).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear receta"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": recipe})
	}
}

func UpdateRecipe(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ProductName *string  `json:"product_name"`
		Category    *string  `json:"category"`
		SalePrice   *float64 `json:"sale_price"`
		Emoji       *string  `json:"emoji"`
		PhotoURL    *string  `json:"photo_url"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var recipe models.Recipe
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&recipe).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "receta no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.ProductName != nil {
			updates["product_name"] = *req.ProductName
		}
		if req.Category != nil {
			updates["category"] = *req.Category
		}
		if req.SalePrice != nil {
			updates["sale_price"] = *req.SalePrice
		}
		if req.Emoji != nil {
			updates["emoji"] = *req.Emoji
		}
		if req.PhotoURL != nil {
			updates["photo_url"] = *req.PhotoURL
		}

		if err := db.Model(&recipe).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar receta"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": recipe})
	}
}

func DeleteRecipe(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		result := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).Delete(&models.Recipe{})
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "receta no encontrada"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "receta eliminada"})
	}
}

func RecipeCost(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var recipe models.Recipe
		if err := db.Preload("Ingredients").
			Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&recipe).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "receta no encontrada"})
			return
		}

		var totalCost float64
		type IngredientCost struct {
			ProductName string  `json:"product_name"`
			Quantity    float64 `json:"quantity"`
			UnitCost    float64 `json:"unit_cost"`
			Subtotal    float64 `json:"subtotal"`
		}

		var details []IngredientCost
		for _, ing := range recipe.Ingredients {
			// Feature 001 — when the line is re-oriented at an
			// Ingredient (insumo), the live cost is the insumo's
			// UnitCost. Legacy lines still pointing at a Product fall
			// back to the product's PurchasePrice (Art. X — old
			// recipes keep working). The stored line UnitCost is the
			// last-resort default for either.
			currentCost := ing.UnitCost
			if ing.IngredientID != nil && *ing.IngredientID != "" {
				var insumo models.Ingredient
				if err := db.Where("id = ? AND tenant_id = ?", *ing.IngredientID, tenantID).
					First(&insumo).Error; err == nil {
					currentCost = insumo.UnitCost
				}
			} else {
				var product models.Product
				if err := db.Where("id = ? AND tenant_id = ?", ing.ProductUUID, tenantID).
					First(&product).Error; err == nil {
					if product.PurchasePrice > 0 {
						currentCost = product.PurchasePrice
					}
				}
			}

			subtotal := currentCost * ing.Quantity
			totalCost += subtotal
			details = append(details, IngredientCost{
				ProductName: ing.ProductName,
				Quantity:    ing.Quantity,
				UnitCost:    currentCost,
				Subtotal:    subtotal,
			})
		}

		profit := recipe.SalePrice - totalCost
		marginPct := float64(0)
		if totalCost > 0 {
			marginPct = (profit / totalCost) * 100
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"recipe_uuid":    recipe.ID,
				"product_name":   recipe.ProductName,
				"sale_price":     recipe.SalePrice,
				"total_cost":     totalCost,
				"profit":         profit,
				"margin_percent": marginPct,
				"ingredients":    details,
			},
		})
	}
}

// RecipeAvailability derives how many units of a product-receta can be
// made from the current insumo stock (FR-05, AC-03):
//
//	available_units = min over insumos of floor(stock / qty_required)
//
// A recipe with no ingredients (or no insumo-oriented lines) has no
// stock constraint and returns -1 to signal "ilimitada" (Spec §9 —
// advertencia, no bloquea). GET /api/v1/recipes/:uuid/availability
func RecipeAvailability(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var recipe models.Recipe
		if err := db.Preload("Ingredients").
			Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&recipe).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "receta no encontrada"})
			return
		}

		// unlimited stays true until the first insumo-oriented line is
		// found — a recipe without insumos has no stock ceiling.
		unlimited := true
		availableUnits := -1

		type IngredientAvailability struct {
			IngredientID string  `json:"ingredient_id"`
			Name         string  `json:"name"`
			Stock        float64 `json:"stock"`
			Required     float64 `json:"required_per_unit"`
			CanMake      int     `json:"can_make"`
		}
		var details []IngredientAvailability

		for _, line := range recipe.Ingredients {
			if line.IngredientID == nil || *line.IngredientID == "" {
				// Legacy line never re-oriented at an insumo — it does
				// not participate in the availability calculation.
				continue
			}
			if line.Quantity <= 0 {
				// A zero-quantity line consumes nothing — skip it so
				// it never forces availability to zero or infinity.
				continue
			}

			var insumo models.Ingredient
			if err := db.Where("id = ? AND tenant_id = ?", *line.IngredientID, tenantID).
				First(&insumo).Error; err != nil {
				// Recipe references a missing/soft-deleted insumo — it
				// cannot be made until the recipe is corrected.
				unlimited = false
				availableUnits = 0
				break
			}

			canMake := int(math.Floor(insumo.Stock / line.Quantity))
			if canMake < 0 {
				canMake = 0
			}
			details = append(details, IngredientAvailability{
				IngredientID: insumo.ID,
				Name:         insumo.Name,
				Stock:        insumo.Stock,
				Required:     line.Quantity,
				CanMake:      canMake,
			})

			if unlimited {
				unlimited = false
				availableUnits = canMake
			} else if canMake < availableUnits {
				availableUnits = canMake
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"recipe_uuid":     recipe.ID,
				"product_name":    recipe.ProductName,
				"available_units": availableUnits,
				"unlimited":       unlimited,
				"ingredients":     details,
			},
		})
	}
}
