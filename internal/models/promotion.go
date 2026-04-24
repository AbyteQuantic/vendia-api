package models

import "time"

// Promotion is a marketing offer owned by a tenant. It can operate in
// two modes that coexist for backward compatibility with pre-migration
// 019 rows:
//
//  1. Single-product legacy mode — the original single-SKU promotion
//     stored entirely on this row (ProductUUID / ProductName /
//     OrigPrice / PromoPrice). Created by the POS "Sugerencias"
//     shortcut. Item rows are not used.
//
//  2. Combo mode — Name describes the combo ("2x1 en gaseosas"),
//     BannerImageURL points to the AI-generated banner, and the actual
//     SKUs + per-unit promo prices live in PromotionItem rows.
//
// StartDate / EndDate gate visibility on the public catalog; StockLimit
// caps redemptions for vigencia "hasta agotar inventario".
type Promotion struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`

	// Combo-mode fields (nullable; legacy rows may leave these empty).
	Name           string     `json:"name,omitempty"`
	StartDate      *time.Time `json:"start_date,omitempty"`
	EndDate        *time.Time `json:"end_date,omitempty"`
	StockLimit     *int       `json:"stock_limit,omitempty"`
	BannerImageURL string     `gorm:"column:banner_image_url" json:"banner_image_url,omitempty"`

	// Legacy single-product fields (nullable in DB since migration 019).
	// ProductUUID is a pointer so combos — which don't have a single
	// SKU — emit SQL NULL instead of the empty string. Without the
	// pointer, GORM tries to INSERT '' into a uuid column and Postgres
	// rejects with SQLSTATE 22P02 ("invalid input syntax for type
	// uuid"). See feedback_nullable_uuid_rule.md.
	ProductUUID *string `gorm:"type:uuid" json:"product_uuid,omitempty"`
	ProductName string  `json:"product_name,omitempty"`
	OrigPrice   float64 `json:"orig_price,omitempty"`
	PromoPrice  float64 `json:"promo_price,omitempty"`

	PromoType   string `gorm:"not null;default:'discount'" json:"promo_type"`
	Description string `json:"description,omitempty"`
	IsActive    bool   `gorm:"default:true" json:"is_active"`

	// ExpiresAt is the legacy single-date expiration (ISO string). New
	// rows should populate EndDate instead; kept for backward compat.
	ExpiresAt *string `json:"expires_at,omitempty"`

	// Items are the combo components. Gorm loads them with Preload.
	// Not persisted directly on the promotions table.
	Items []PromotionItem `gorm:"foreignKey:PromotionID" json:"items,omitempty"`
}

// PromotionItem is one line of a combo promotion — a product with its
// combo quantity and the per-unit promo price. Multiple items per
// promotion, cascaded on promotion delete.
type PromotionItem struct {
	BaseModel

	PromotionID string  `gorm:"type:uuid;not null;index" json:"promotion_id"`
	ProductID   string  `gorm:"type:uuid;not null;index" json:"product_id"`
	Quantity    int     `gorm:"not null;default:1"        json:"quantity"`
	PromoPrice  float64 `gorm:"type:numeric(12,2);not null" json:"promo_price"`
}

// TableName pins the plural form expected by migration 019.
func (PromotionItem) TableName() string { return "promotion_items" }
