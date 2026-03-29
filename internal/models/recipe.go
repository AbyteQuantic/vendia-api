package models

type Recipe struct {
	BaseModel

	TenantID    string             `gorm:"type:uuid;not null;index" json:"tenant_id"`
	ProductName string             `gorm:"not null" json:"product_name"`
	Category    string             `json:"category,omitempty"`
	SalePrice   float64            `gorm:"not null" json:"sale_price"`
	Emoji       string             `json:"emoji,omitempty"`
	PhotoURL    string             `json:"photo_url,omitempty"`
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
}
