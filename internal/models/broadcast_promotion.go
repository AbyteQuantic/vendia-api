// Spec: specs/033-difusion-promociones/spec.md
package models

import "time"

// BroadcastPromotion is a customer-facing marketing campaign owned by a
// tenant (Spec F033). It is a NEW module, deliberately separate from the
// legacy combo-promo `Promotion` model (migraciones 018-019): that one
// drives the public catalog carousel and the POS price override, while
// this one drives segmented WhatsApp/link broadcasts to identified
// customers (F030).
//
// The two coexist. LegacyComboID is an optional FK so a tenant who built
// a combo in the old module can attach it to a broadcast campaign without
// duplicating data; it is nullable and is stored as *string per the repo
// convention so GORM emits SQL NULL instead of an empty string into a
// uuid column (see feedback_nullable_uuid_rule.md).
//
// Lifecycle: a promotion is created in `draft`, gains PromotionDeliveries
// once the owner picks an audience and a channel, and exposes a public
// page via PublicToken. ValidFrom/ValidUntil gate that public page.
type BroadcastPromotion struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`

	Title       string `gorm:"type:varchar(200);not null" json:"title"`
	Description string `gorm:"type:text" json:"description,omitempty"`
	ImageURL    string `gorm:"type:varchar(500)" json:"image_url,omitempty"`
	CouponCode  string `gorm:"type:varchar(30)" json:"coupon_code,omitempty"`

	// MessageTemplate is the WhatsApp copy base. It may carry the
	// {nombre} / {primer_nombre} placeholders the Flutter queue (and the
	// audience pre-generation) substitute per customer. Empty is allowed.
	MessageTemplate string `gorm:"type:text" json:"message_template,omitempty"`

	ValidFrom  time.Time `gorm:"not null" json:"valid_from"`
	ValidUntil time.Time `gorm:"not null" json:"valid_until"`

	// ScheduledFor is null for "enviar ahora"; when set, the
	// promotions-push job notifies the owner at that time so the
	// assisted queue is ready (Spec F033 §4.5 #5). SchedulePushSent
	// marks the row so the job never fires the reminder twice.
	ScheduledFor     *time.Time `json:"scheduled_for,omitempty"`
	SchedulePushSent bool       `gorm:"not null;default:false" json:"schedule_push_sent"`

	// PublicToken is the unguessable UUID v4 credential for the public
	// promo page (tienda.vendia.store/p/<token>). Same pattern as the
	// public quote/fiado links. VisitCount is incremented on every
	// public GET — basic analytics, no per-visit dedup (plan D4).
	PublicToken string `gorm:"type:uuid;not null;uniqueIndex" json:"public_token"`
	VisitCount  int    `gorm:"not null;default:0" json:"visit_count"`

	// LegacyComboID optionally links this campaign to a row in the
	// legacy `promotions` (combo) table. Nullable — *string so combos
	// without a link emit SQL NULL.
	LegacyComboID *string `gorm:"type:uuid" json:"legacy_combo_id,omitempty"`

	// IsActive lets the owner soft-disable a campaign without deleting
	// it. Default true on create.
	IsActive bool `gorm:"not null;default:true" json:"is_active"`

	// Items are the products on offer. Loaded with Preload, cascaded on
	// delete via the handler transaction.
	Items []BroadcastPromotionItem `gorm:"foreignKey:PromotionID" json:"items,omitempty"`
}

// TableName pins the table so it never collides with the legacy
// `promotions` / `promotion_items` tables.
func (BroadcastPromotion) TableName() string { return "broadcast_promotions" }

// BroadcastPromotionItem is one product on offer inside a broadcast
// promotion. Exactly one of PromoPrice / DiscountPct should be set:
// PromoPrice is a fixed promotional price, DiscountPct is a percentage
// off the shelf price. Both are nullable pointers so "not specified" is
// distinguishable from "zero".
type BroadcastPromotionItem struct {
	BaseModel

	PromotionID string `gorm:"type:uuid;not null;index" json:"promotion_id"`
	ProductID   string `gorm:"type:uuid;not null;index" json:"product_id"`

	// PromoPrice — fixed promotional price in COP. Nil when the item
	// uses a percentage discount instead.
	PromoPrice *float64 `gorm:"type:numeric(12,2)" json:"promo_price,omitempty"`
	// DiscountPct — percentage off the shelf price (0..100). Nil when
	// the item uses a fixed price instead.
	DiscountPct *float64 `gorm:"type:numeric(5,2)" json:"discount_pct,omitempty"`
}

// TableName pins the plural form, separate from the legacy
// `promotion_items` combo table.
func (BroadcastPromotionItem) TableName() string { return "broadcast_promotion_items" }

// Broadcast delivery channels.
const (
	PromotionChannelWhatsApp = "whatsapp"
	PromotionChannelLink     = "link"
	PromotionChannelManual   = "manual"
	PromotionChannelQR       = "qr"
)

// Broadcast delivery statuses.
const (
	PromotionDeliveryQueued  = "queued"
	PromotionDeliverySent    = "sent"
	PromotionDeliverySkipped = "skipped"
)

// ValidPromotionChannels is the whitelist the handler enforces so an
// invalid channel fails with a Spanish 400 instead of a DB error.
var ValidPromotionChannels = map[string]struct{}{
	PromotionChannelWhatsApp: {},
	PromotionChannelLink:     {},
	PromotionChannelManual:   {},
	PromotionChannelQR:       {},
}

// BroadcastPromotionDelivery is one (promotion, customer, channel)
// broadcast record. It tracks whether the owner actually sent the
// message (Status), when (SentAt), and whether the customer opened the
// public link (VisitedAt).
//
// The uniqueIndex on (promotion_id, customer_id, channel) prevents the
// same customer being queued twice for the same campaign on the same
// channel — the anti-spam invariant of plan D2.
type BroadcastPromotionDelivery struct {
	BaseModel

	PromotionID string `gorm:"type:uuid;not null;index:idx_bpd_promo;uniqueIndex:uq_bpd_promo_customer_channel,priority:1" json:"promotion_id"`
	CustomerID  string `gorm:"type:uuid;not null;index:idx_bpd_customer;uniqueIndex:uq_bpd_promo_customer_channel,priority:2" json:"customer_id"`
	Channel     string `gorm:"type:varchar(20);not null;uniqueIndex:uq_bpd_promo_customer_channel,priority:3" json:"channel"`

	Status string `gorm:"type:varchar(20);not null;default:'queued'" json:"status"`

	SentAt    *time.Time `json:"sent_at,omitempty"`
	VisitedAt *time.Time `json:"visited_at,omitempty"`

	// Customer is the optional eager relation for the assisted-queue
	// payload (name + phone). Never exposed through a public endpoint.
	Customer *Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
}

// TableName pins the table name.
func (BroadcastPromotionDelivery) TableName() string {
	return "broadcast_promotion_deliveries"
}
