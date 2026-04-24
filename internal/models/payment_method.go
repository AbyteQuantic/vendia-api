package models

// TenantPaymentMethod represents a configured payment method for a business.
//
// Schema evolution (2026-04): the original columns (`name`,
// `account_details`, `is_active`) stay untouched — the PaymentQuickSetup
// flow, `payments.go` handler, `fiado.go` exposure and every POS query
// already depend on them.
//
// Two new OPTIONAL columns are added via AutoMigrate (never backfilled,
// never required):
//   - `provider`      — normalised wallet id ("nequi","daviplata",
//     "bancolombia","breve","efectivo","otro"). Fallback comes from
//     `name` for pre-migration rows so nothing breaks.
//   - `qr_image_url`  — public URL of a QR code image uploaded to
//     Cloudflare R2 / Supabase Storage. Nullable.
//
// Public catalog (`GET /public/catalog/:slug`) exposes the subset
// where is_active = true so end customers can pay without asking.
type TenantPaymentMethod struct {
	BaseModel

	TenantID       string `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Name           string `gorm:"not null" json:"name"`
	AccountDetails string `gorm:"default:''" json:"account_details"`
	IsActive       bool   `gorm:"default:true" json:"is_active"`

	// Normalised wallet provider id (lowercase, URL-safe). Optional —
	// pre-migration rows leave it empty and clients can fall back to
	// the lowercased `name`.
	Provider string `gorm:"type:varchar(32);default:''" json:"provider"`

	// Public URL of an uploaded QR code (PNG/JPEG on R2/Supabase).
	// Nullable string column — empty means "no QR, the number alone
	// is enough".
	QRImageURL string `gorm:"column:qr_image_url;default:''" json:"qr_image_url"`
}

// TableName overrides GORM default to use payment_methods.
func (TenantPaymentMethod) TableName() string {
	return "payment_methods"
}
