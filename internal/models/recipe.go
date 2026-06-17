package models

type Recipe struct {
	BaseModel

	TenantID    string             `gorm:"type:uuid;not null;index" json:"tenant_id"`
	ProductName string             `gorm:"not null" json:"product_name"`
	Category    string             `json:"category,omitempty"`
	SalePrice   float64            `gorm:"not null" json:"sale_price"`
	Emoji       string             `json:"emoji,omitempty"`
	PhotoURL    string             `json:"photo_url,omitempty"`
	// ProductID (Feature 001) links the recipe to the vendible
	// Product it produces. Nullable *string so legacy recipes that
	// predate the feature keep working (Art. X); set when a
	// product-receta is wired up.
	ProductID   *string            `gorm:"type:uuid;index" json:"product_id,omitempty"`
	Ingredients []RecipeIngredient `gorm:"foreignKey:RecipeUUID;references:ID" json:"ingredients"`
	// Spec 065 — Recipe Studio. Metadatos de preparación, ADITIVOS y
	// retrocompatibles (Art. X). NO tocan el costeo (que sigue derivándose
	// solo de Ingredients). Vacíos en recetas legacy.
	//   - Yield:    rendimiento, p. ej. "10 porciones".
	//   - PrepTime: tiempo de preparación, p. ej. "30 min".
	//   - PrepSteps: pasos como JSON array de [{ "text": "...", "photo_url": "..." }],
	//     guardado como JSONB string (mismo patrón que OnlineOrder.Items).
	Yield     string `gorm:"default:''" json:"yield"`
	PrepTime  string `gorm:"default:''" json:"prep_time"`
	PrepSteps string `gorm:"type:jsonb;default:'[]'" json:"prep_steps"`
}

type RecipeIngredient struct {
	BaseModel

	RecipeUUID string `gorm:"type:uuid;not null;index" json:"recipe_uuid"`
	// ProductUUID is the legacy product-oriented anchor. Feature 001
	// re-orients recipe lines at an Ingredient (insumo), so new lines
	// leave this nil. It is a nullable *string (was `not null`) so the
	// new insumo contract never has to fabricate a meaningless product
	// UUID — AutoMigrate drops the NOT NULL constraint additively and
	// retrocompatibly (Art. X). Legacy rows that still carry a value
	// keep working via the RecipeCost fallback branch.
	ProductUUID *string `gorm:"type:uuid" json:"product_uuid,omitempty"`
	ProductName string  `gorm:"not null" json:"product_name"`
	Quantity    float64 `gorm:"not null" json:"quantity"`
	UnitCost    float64 `gorm:"not null" json:"unit_cost"`
	Emoji       string  `json:"emoji,omitempty"`
	// IngredientID (Feature 001) re-orients the recipe line at an
	// Ingredient (insumo) instead of a Product. The JSON tag is
	// `ingredient_uuid` for consistency with `recipe_uuid` /
	// `product_uuid`. The explosion prefers IngredientID and falls
	// back to nothing when it is nil.
	IngredientID *string `gorm:"type:uuid;index" json:"ingredient_uuid,omitempty"`
}
