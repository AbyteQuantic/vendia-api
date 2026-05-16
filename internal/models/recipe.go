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
}

type RecipeIngredient struct {
	BaseModel

	RecipeUUID  string  `gorm:"type:uuid;not null;index" json:"recipe_uuid"`
	ProductUUID string  `gorm:"type:uuid;not null" json:"product_uuid"`
	ProductName string  `gorm:"not null" json:"product_name"`
	Quantity    float64 `gorm:"not null" json:"quantity"`
	UnitCost    float64 `gorm:"not null" json:"unit_cost"`
	Emoji       string  `json:"emoji,omitempty"`
	// IngredientID (Feature 001) re-orients the recipe line at an
	// Ingredient (insumo) instead of a Product. ProductUUID is
	// CONSCIOUSLY kept (Plan §6, Art. X) so existing data and older
	// clients are not broken — the explosion prefers IngredientID
	// and falls back to nothing when it is nil.
	IngredientID *string `gorm:"type:uuid;index" json:"ingredient_id,omitempty"`
}
