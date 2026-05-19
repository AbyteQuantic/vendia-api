package models

import "time"

// Unified business-type taxonomy (see migration 020).
// The DB enforces the same whitelist via validate_business_types().
// Keep this list in sync with DefaultFeatureFlags and with the Flutter
// StepConfig options — changes MUST land as a migration.
const (
	BusinessTypeTiendaBarrio         = "tienda_barrio"
	BusinessTypeMinimercado          = "minimercado"
	BusinessTypeDepositoConstruccion = "deposito_construccion"
	BusinessTypeRestaurante          = "restaurante"
	BusinessTypeComidasRapidas       = "comidas_rapidas"
	BusinessTypeBar                  = "bar"
	BusinessTypeManufactura          = "manufactura"
	BusinessTypeReparacionMuebles    = "reparacion_muebles"
	BusinessTypeEmprendimientoGen    = "emprendimiento_general"
)

// ValidBusinessTypes is the canonical whitelist. The register handler
// rejects anything outside it (defense in depth — the DB CHECK would
// reject it too, but surfacing the error at the app layer gives a
// Spanish message instead of a 500).
var ValidBusinessTypes = map[string]struct{}{
	BusinessTypeTiendaBarrio:         {},
	BusinessTypeMinimercado:          {},
	BusinessTypeDepositoConstruccion: {},
	BusinessTypeRestaurante:          {},
	BusinessTypeComidasRapidas:       {},
	BusinessTypeBar:                  {},
	BusinessTypeManufactura:          {},
	BusinessTypeReparacionMuebles:    {},
	BusinessTypeEmprendimientoGen:    {},
}

// FeatureFlags are per-tenant module toggles derived from business_types.
// The frontend reads this blob at login and hides modules accordingly.
// Storing the booleans explicitly (vs. re-deriving them client-side) lets
// us ship admin overrides without an app release — see migration 021.
type FeatureFlags struct {
	EnableTables          bool `json:"enable_tables"`
	EnableKDS             bool `json:"enable_kds"`
	EnableTips            bool `json:"enable_tips"`
	EnableServices        bool `json:"enable_services"`
	EnableCustomBilling   bool `json:"enable_custom_billing"`
	EnableFractionalUnits bool `json:"enable_fractional_units"`
}

// CapabilityToggles carries the three optional capability toggles that a
// merchant can activate independently of their business type (Spec F023).
// Tables replaces the old hasTables bool — all callers must migrate.
type CapabilityToggles struct {
	// Tables grants enable_tables WITHOUT enabling KDS or Tips (those
	// remain exclusive to food-type businesses).
	Tables bool
	// Services grants enable_services + enable_custom_billing.
	Services bool
	// FractionalUnits grants enable_fractional_units.
	FractionalUnits bool
}

// DefaultFeatureFlags computes the feature flag matrix from a list of
// business types combined with explicit capability toggles (Spec F023).
// The result is (type-implied capabilities) OR (opts toggles) — toggles
// can only add capabilities, never remove type-implied ones.
// Keep this in sync with the SQL backfill in migration 021 — they must
// produce identical output for opts == CapabilityToggles{}.
func DefaultFeatureFlags(types []string, opts CapabilityToggles) FeatureFlags {
	has := func(needles ...string) bool {
		for _, n := range needles {
			for _, t := range types {
				if t == n {
					return true
				}
			}
		}
		return false
	}

	food := has(BusinessTypeRestaurante, BusinessTypeComidasRapidas, BusinessTypeBar)
	services := has(BusinessTypeReparacionMuebles, BusinessTypeManufactura, BusinessTypeEmprendimientoGen)

	return FeatureFlags{
		EnableTables:          food || opts.Tables,
		EnableKDS:             food, // KDS stays exclusive to food — D3
		EnableTips:            food, // Tips stays exclusive to food — D3
		EnableServices:        services || opts.Services,
		EnableCustomBilling:   services || opts.Services,
		EnableFractionalUnits: has(BusinessTypeDepositoConstruccion) || opts.FractionalUnits,
	}
}

type Tenant struct {
	BaseModel

	OwnerName    string `gorm:"not null" json:"owner_name"`
	Phone        string `gorm:"not null;uniqueIndex" json:"phone"`
	PasswordHash string `gorm:"not null" json:"-"`
	// OwnerPinHash is bcrypt-hashed 4-digit PIN used to authorize cashier
	// actions that require owner approval (e.g. new fiado for unknown customer).
	OwnerPinHash string `gorm:"default:''" json:"-"`

	BusinessName  string   `gorm:"not null" json:"business_name"`
	BusinessTypes []string `gorm:"serializer:json;default:'[]'" json:"business_types"`
	// FeatureFlags is the derived per-tenant module toggle blob. Backend
	// computes it on register via DefaultFeatureFlags; frontend reads it
	// at login to decide which modules to render. Stored as JSONB (see
	// migration 021) but GORM only needs the serializer directive.
	FeatureFlags FeatureFlags `gorm:"serializer:json;type:jsonb;not null;default:'{}'" json:"feature_flags"`
	RazonSocial  string       `gorm:"not null;default:''" json:"razon_social"`
	NIT          string       `gorm:"not null;default:''" json:"nit"`
	Address      string       `gorm:"not null;default:''" json:"address"`

	SaleTypes    []string `gorm:"serializer:json;not null" json:"sale_types"`
	HasShowcases bool     `gorm:"not null;default:false" json:"has_showcases"`
	HasTables    bool     `gorm:"not null;default:false" json:"has_tables"`

	ChargeMode          string  `gorm:"default:'pre_payment'" json:"charge_mode"`
	EnableFiados        bool    `gorm:"default:true" json:"enable_fiados"`
	DefaultMargin       float64 `gorm:"default:20" json:"default_margin"`
	PanicMessage        string  `gorm:"default:''" json:"panic_message"`
	PanicIncludeAddress bool    `gorm:"default:true" json:"panic_include_address"`
	PanicIncludeGPS     bool    `gorm:"default:true" json:"panic_include_gps"`
	Latitude            float64 `gorm:"default:0" json:"latitude"`
	Longitude           float64 `gorm:"default:0" json:"longitude"`

	NequiPhone     *string `gorm:"size:15" json:"nequi_phone"`
	DaviplataPhone *string `gorm:"size:15" json:"daviplata_phone"`

	// Express payment config (2026-04-20 pivot: Nequi rejected our
	// QR deep link, so the public fiado portal now shows account info
	// with copy buttons). Stored on the tenant row for zero-join
	// reads from the public endpoint. Empty strings mean not configured.
	PaymentMethodName    string `gorm:"type:varchar(32);not null;default:''" json:"payment_method_name"`
	PaymentAccountNumber string `gorm:"type:varchar(64);not null;default:''" json:"payment_account_number"`
	PaymentAccountHolder string `gorm:"type:varchar(128);not null;default:''" json:"payment_account_holder"`

	LastSyncAt     *time.Time `json:"last_sync_at"`
	PendingSyncOps int        `gorm:"default:0" json:"pending_sync_ops"`

	// DEPRECATED (audit H18). The canonical source of truth is the
	// `tenant_subscriptions` table — see [TenantSubscription] +
	// `middleware.PremiumAuth`. These two fields used to coexist
	// with that table and could drift (dashboard read "TRIAL" while
	// the endpoint blocked as "FREE").
	//
	// `json:"-"` keeps the columns alive in the database (so a
	// rollback that brings back the legacy handlers still has the
	// data) but prevents them from being serialised in any API
	// response. A future migration will drop the columns altogether
	// once we are confident no rollback path needs them.
	SubscriptionStatus string     `gorm:"default:'trial'" json:"-"`
	SubscriptionEndsAt *time.Time `json:"-"`

	// Printer / Receipts
	ReceiptHeader     string `gorm:"default:''" json:"receipt_header"`
	ReceiptFooter     string `gorm:"default:''" json:"receipt_footer"`
	PrinterMacAddress string `gorm:"default:''" json:"printer_mac_address"`

	// Store / Delivery
	StoreSlug      *string `gorm:"uniqueIndex" json:"store_slug,omitempty"`
	IsDeliveryOpen bool    `gorm:"default:false" json:"is_delivery_open"`
	DeliveryCost   float64 `gorm:"default:0" json:"delivery_cost"`
	MinOrderAmount float64 `gorm:"default:0" json:"min_order_amount"`
	LogoURL        string  `json:"logo_url,omitempty"`

	// ── IVA / Growth Radar (epic Safe Tax Flow) ───────────────────
	// VATEnabled is the master switch for IVA flow. Frontend reads this
	// to drive the snapshot population on closed sales.
	VATEnabled *bool `gorm:"default:false" json:"vat_enabled"`

	// VATRate is the VAT rate as a decimal (0.19 = 19%). Nullable so a
	// never-activated tenant has no value at all.
	VATRate *float64 `json:"vat_rate"`

	// VATInclusivePricing controls whether stored prices already
	// include VAT (Colombia default = true) or VAT is added at checkout.
	VATInclusivePricing *bool `json:"vat_inclusive_pricing"`

	// VATActivatedAt records when the merchant first turned VAT on.
	// Used for audit + the "una vez activado, siempre activo" rule.
	VATActivatedAt *time.Time `json:"vat_activated_at"`

	// DIANThresholdCOP overrides the per-tenant Growth Radar threshold.
	// Nullable — when null, frontend uses the global default (160_000_000).
	DIANThresholdCOP *int64 `json:"dian_threshold_cop"`

	Employees []Employee `gorm:"foreignKey:TenantID" json:"employees,omitempty"`
}
