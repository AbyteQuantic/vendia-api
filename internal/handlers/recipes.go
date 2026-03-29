package handlers

import (
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
			var product models.Product
			currentCost := ing.UnitCost
			if err := db.Where("id = ? AND tenant_id = ?", ing.ProductUUID, tenantID).
				First(&product).Error; err == nil {
				if product.PurchasePrice > 0 {
					currentCost = product.PurchasePrice
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
