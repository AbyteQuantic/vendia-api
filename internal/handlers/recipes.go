package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StepInput is one preparation step from the Recipe Studio (Spec 065).
// Ordered as received. Photo is optional (a URL already uploaded).
type StepInput struct {
	Text     string `json:"text"`
	PhotoURL string `json:"photo_url"`
}

// recipeIngredientInput is the insumo contract for editing a recipe's
// ingredient set via UpdateRecipe (Spec 065). Same shape as CreateRecipe's
// inline IngredientInput; name/unit cost are snapshotted server-side.
type recipeIngredientInput struct {
	IngredientUUID string  `json:"ingredient_uuid"`
	Quantity       float64 `json:"quantity"`
}

// marshalSteps serializes the ordered steps to a JSON array string for the
// Recipe.PrepSteps JSONB column. Empty/blank steps are dropped so the stored
// list stays clean. Always returns valid JSON ("[]" when there are none) so
// the column never holds NULL/garbage.
func marshalSteps(steps []StepInput) string {
	clean := make([]StepInput, 0, len(steps))
	for _, s := range steps {
		if strings.TrimSpace(s.Text) == "" && strings.TrimSpace(s.PhotoURL) == "" {
			continue
		}
		clean = append(clean, StepInput{
			Text:     strings.TrimSpace(s.Text),
			PhotoURL: strings.TrimSpace(s.PhotoURL),
		})
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return "[]"
	}
	return string(b)
}

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
	// IngredientInput is the Feature 001 insumo contract: a recipe line
	// references an Ingredient (insumo) by UUID plus the quantity it
	// consumes. Name and unit cost are NOT trusted from the client —
	// they are snapshotted server-side from the resolved insumo.
	type IngredientInput struct {
		IngredientUUID string  `json:"ingredient_uuid" binding:"required"`
		Quantity       float64 `json:"quantity"        binding:"required,gt=0"`
	}

	type Request struct {
		ID          string  `json:"id"`
		ProductName string  `json:"product_name" binding:"required"`
		Category    string  `json:"category"`
		SalePrice   float64 `json:"sale_price"   binding:"required,gt=0"`
		Emoji       string  `json:"emoji"`
		PhotoURL    string  `json:"photo_url"`
		// F043 — el plato/receta también es un ítem del menú: la
		// descripción apetitosa y la porción alimentan la tarjeta del
		// catálogo público. Opcionales; viajan al Product, no a la Recipe
		// (que solo modela el costo).
		Description string `json:"description"`
		Portion     string `json:"portion"`
		// Spec 065 — Recipe Studio: metadatos de preparación (aditivos, no
		// tocan el costeo). PrepSteps es un array de pasos; se serializa a
		// JSON y se guarda en Recipe.PrepSteps.
		Yield     string      `json:"yield"`
		PrepTime  string      `json:"prep_time"`
		PrepSteps []StepInput `json:"prep_steps"`
		// `dive` makes the validator descend into each slice element so
		// the per-field rules on IngredientInput (required ingredient_uuid,
		// quantity > 0) are actually enforced.
		Ingredients []IngredientInput `json:"ingredients"  binding:"required,min=1,dive"`
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
			// Resolve the insumo tenant-scoped (Art. III). A miss means
			// the recipe references an insumo that does not exist for
			// this negocio — reject the whole request.
			var insumo models.Ingredient
			if err := db.Where("id = ? AND tenant_id = ?", ing.IngredientUUID, tenantID).
				First(&insumo).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "el insumo no existe: " + ing.IngredientUUID,
				})
				return
			}

			// Snapshot the insumo's name and unit cost onto the recipe
			// line so the receta keeps a stable historic record even if
			// the insumo is later renamed or repriced.
			ingredientID := insumo.ID
			ingredients = append(ingredients, models.RecipeIngredient{
				IngredientID: &ingredientID,
				ProductName:  insumo.Name,
				Quantity:     ing.Quantity,
				UnitCost:     insumo.UnitCost,
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
			Yield:       req.Yield,
			PrepTime:    req.PrepTime,
			PrepSteps:   marshalSteps(req.PrepSteps),
		}
		if req.ID != "" {
			recipe.ID = req.ID
		}

		// FR-02 — a receta vincula un producto vendible. The recipe and
		// its vendible Product are created in ONE transaction so the POS
		// can never see a recipe without a sellable product, nor an
		// orphan product if the recipe write fails (Art. VII, Art. IX).
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&recipe).Error; err != nil {
				return err
			}

			recipeID := recipe.ID
			// The vendible product-receta. IsRecipe flips the POS sale
			// path to ExplodeRecipe; Stock stays 0 because availability
			// is derived from the insumos (D1 — disponibilidad derivada).
			product := models.Product{
				TenantID:    tenantID,
				Name:        recipe.ProductName,
				Price:       recipe.SalePrice,
				Category:    recipe.Category,
				Emoji:       recipe.Emoji,
				PhotoURL:    recipe.PhotoURL,
				Stock:       0,
				IsAvailable: true,
				IsRecipe:    true,
				RecipeID:    &recipeID,
				// F043 — un plato/receta también es un ítem del menú del
				// restaurante: alimenta la sección "Menú restaurante" del catálogo.
				IsMenuItem: true,
				// Descripción y porción para la tarjeta del catálogo (F043
				// slice manual: foto + descripción + porción).
				Description: req.Description,
				Portion:     req.Portion,
			}
			if err := tx.Create(&product).Error; err != nil {
				return err
			}

			// Close the loop: Recipe.ProductID → the vendible Product.
			productID := product.ID
			recipe.ProductID = &productID
			return tx.Model(&recipe).
				Where("id = ? AND tenant_id = ?", recipe.ID, tenantID).
				UpdateColumn("product_id", productID).Error
		}); err != nil {
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
		// Spec 065 — metadatos de preparación (aditivos, no tocan el costeo).
		Yield     *string     `json:"yield"`
		PrepTime  *string     `json:"prep_time"`
		PrepSteps []StepInput `json:"prep_steps"`
		// Spec 065 — editar la receta en el Studio puede cambiar los insumos.
		// Si `ingredients` viaja (no nil), se REEMPLAZA el set completo. La
		// resolución/snapshot es idéntica a CreateRecipe — el costeo NO cambia,
		// solo qué insumos componen la receta. nil ⇒ no se tocan los insumos.
		Ingredients []recipeIngredientInput `json:"ingredients"`
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
		// Spec 065 — metadatos de preparación. PrepSteps != nil ⇒ el cliente
		// envió la lista (aunque sea vacía) y la persistimos serializada.
		if req.Yield != nil {
			updates["yield"] = *req.Yield
		}
		if req.PrepTime != nil {
			updates["prep_time"] = *req.PrepTime
		}
		if req.PrepSteps != nil {
			updates["prep_steps"] = marshalSteps(req.PrepSteps)
		}

		// Spec 065 — si llegan insumos, los pre-resolvemos ANTES de la
		// transacción para poder responder 400 con un mensaje claro si alguno
		// no existe (igual que CreateRecipe). nil ⇒ no se tocan los insumos.
		var replacementIngredients []models.RecipeIngredient
		replaceIngredients := req.Ingredients != nil
		if replaceIngredients {
			for _, ing := range req.Ingredients {
				if strings.TrimSpace(ing.IngredientUUID) == "" || ing.Quantity <= 0 {
					continue
				}
				var insumo models.Ingredient
				if err := db.Where("id = ? AND tenant_id = ?", ing.IngredientUUID, tenantID).
					First(&insumo).Error; err != nil {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": "el insumo no existe: " + ing.IngredientUUID,
					})
					return
				}
				iid := insumo.ID
				replacementIngredients = append(replacementIngredients, models.RecipeIngredient{
					RecipeUUID:   recipe.ID,
					IngredientID: &iid,
					ProductName:  insumo.Name,
					Quantity:     ing.Quantity,
					UnitCost:     insumo.UnitCost,
				})
			}
		}

		// FR-02 — keep the linked vendible Product's Name/Price/Category/
		// Emoji in sync with the recipe so the POS shows the up-to-date
		// plato. Done in the same transaction as the recipe update so the
		// two never drift apart.
		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(updates) > 0 {
				if err := tx.Model(&recipe).Updates(updates).Error; err != nil {
					return err
				}
			}
			// Reemplazo del set de insumos (Spec 065): borra los actuales e
			// inserta los nuevos resueltos. El costeo se sigue derivando de
			// estos (unitCost·quantity) — no cambia el cálculo.
			if replaceIngredients {
				if err := tx.Where("recipe_uuid = ?", recipe.ID).
					Delete(&models.RecipeIngredient{}).Error; err != nil {
					return err
				}
				if len(replacementIngredients) > 0 {
					if err := tx.Create(&replacementIngredients).Error; err != nil {
						return err
					}
				}
			}
			if recipe.ProductID == nil || *recipe.ProductID == "" {
				// Legacy recipe with no linked product (predates FR-02):
				// nothing to sync (Art. X — old recipes keep working).
				return nil
			}
			productUpdates := map[string]any{}
			if req.ProductName != nil {
				productUpdates["name"] = *req.ProductName
			}
			if req.SalePrice != nil {
				productUpdates["price"] = *req.SalePrice
			}
			if req.Category != nil {
				productUpdates["category"] = *req.Category
			}
			if req.Emoji != nil {
				productUpdates["emoji"] = *req.Emoji
			}
			if req.PhotoURL != nil {
				productUpdates["photo_url"] = *req.PhotoURL
			}
			if len(productUpdates) == 0 {
				return nil
			}
			return tx.Model(&models.Product{}).
				Where("id = ? AND tenant_id = ?", *recipe.ProductID, tenantID).
				Updates(productUpdates).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar receta"})
			return
		}

		// Recargar con los insumos para que la respuesta refleje el estado real
		// (incluido el set reemplazado).
		db.Preload("Ingredients").
			Where("id = ? AND tenant_id = ?", uuid, tenantID).First(&recipe)
		c.JSON(http.StatusOK, gin.H{"data": recipe})
	}
}

func DeleteRecipe(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		// Load the recipe first so we know which vendible Product it is
		// linked to (FR-02). Deleting blind would leave an orphan
		// product-receta sellable in the POS.
		var recipe models.Recipe
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&recipe).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "receta no encontrada"})
			return
		}

		// Soft-delete the recipe AND its linked product in one
		// transaction so the POS never keeps a sellable product whose
		// recipe is gone (Art. IX — coherent state).
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("id = ? AND tenant_id = ?", uuid, tenantID).
				Delete(&models.Recipe{}).Error; err != nil {
				return err
			}
			if recipe.ProductID == nil || *recipe.ProductID == "" {
				// Legacy recipe with no linked product (predates FR-02).
				return nil
			}
			return tx.Where("id = ? AND tenant_id = ?", *recipe.ProductID, tenantID).
				Delete(&models.Product{}).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar receta"})
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
			} else if ing.ProductUUID != nil && *ing.ProductUUID != "" {
				var product models.Product
				if err := db.Where("id = ? AND tenant_id = ?", *ing.ProductUUID, tenantID).
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
