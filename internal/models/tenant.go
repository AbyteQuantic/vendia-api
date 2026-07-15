package models

import "time"

// CatalogTermsVersion — versión vigente de los Términos que incluye la cláusula
// de uso COLABORATIVO de imágenes (Spec 098). Un tenant cuyo
// TermsAcceptedVersion sea distinto debe re-aceptar en el próximo ingreso.
// Cambiar esta constante cuando la cláusula cambie fuerza re-aceptación global.
// v3 (2026-07-09, Adenda B): Términos completos aterrizados en la ley colombiana
// (Ley 1581 habeas data, Ley 1480 consumidor, Ley 527 comercio electrónico,
// derechos de autor) + Política de Privacidad. Subir la versión fuerza
// re-aceptación de todos los tenants.
const CatalogTermsVersion = "2026-07-09"

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
	// F042 — academias / institutos: organizan cursos, conferencias y
	// hackatones, así que su tipo implica la capacidad de Eventos.
	BusinessTypeAcademias = "academias_instituciones"
	// Spec 075 — proveedores B2B: venden a las tiendas cercanas.
	// Agrícola = agricultor/productor (perecederos por cosecha);
	// mayorista = comercializa abarrotes/aceite (no perecedero).
	BusinessTypeProveedorAgricola  = "proveedor_agricola"
	BusinessTypeProveedorMayorista = "proveedor_mayorista"
	// Spec 084 — peluquerías, barberías y salones de belleza: venden SERVICIOS
	// atendidos por profesionales, con turnos/citas y liquidación por comisión/
	// arriendo de silla/sueldo. Su tipo implica la capacidad de Servicios.
	BusinessTypePeluqueria = "peluqueria_barberia"
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
	BusinessTypeAcademias:            {},
	BusinessTypeProveedorAgricola:    {},
	BusinessTypeProveedorMayorista:   {},
	BusinessTypePeluqueria:           {},
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
	// EnableEvents gates the Events module (Spec F042). Self-activated by
	// the tenant from settings / the "descubre más opciones" reel — never
	// type-implied, so DefaultFeatureFlags leaves it false (decision #2).
	EnableEvents bool `json:"enable_events"`
	// EnableSupplierMode gates el modo "Vendo a tiendas" (Spec 075): catálogo
	// para tiendas cercanas, perecederos, difusión. Lo prenden los business_type
	// proveedor_agricola / proveedor_mayorista.
	EnableSupplierMode bool `json:"enable_supplier_mode"`
	// EnableStaffCommissions gatilla la atribución de servicios por profesional y
	// la liquidación por comisión/arriendo/sueldo (Spec 084). Type-implied para
	// peluquerías/barberías; cualquier tenant puede activarlo (p. ej. comisión a
	// meseros). Solo AGREGA capacidad (Art. X).
	EnableStaffCommissions bool `json:"enable_staff_commissions"`
	// Spec 105 F3 — "el mesero puede cobrar": OFF por defecto (mesero puro);
	// ON habilita cobro en la vista del mesero. Nunca type-implied — decisión
	// explícita del dueño (fundador 2026-07-14 #1).
	EnableWaiterCharge bool `json:"enable_waiter_charge"`
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
	services := has(BusinessTypeReparacionMuebles, BusinessTypeManufactura, BusinessTypeEmprendimientoGen, BusinessTypePeluqueria)
	supplier := has(BusinessTypeProveedorAgricola, BusinessTypeProveedorMayorista)
	// Spec 084 — peluquería/barbería implica liquidación a profesionales.
	salon := has(BusinessTypePeluqueria)

	return FeatureFlags{
		EnableSupplierMode:    supplier,
		EnableTables:          food || opts.Tables,
		EnableKDS:             food, // KDS stays exclusive to food — D3
		EnableTips:            food, // Tips stays exclusive to food — D3
		EnableServices:        services || opts.Services,
		EnableCustomBilling:   services || opts.Services,
		EnableFractionalUnits: has(BusinessTypeDepositoConstruccion) || opts.FractionalUnits,
		// F042 — academias/institutos implican Eventos. Otros tipos lo
		// activan self-service desde el reel (queda false aquí).
		EnableEvents: has(BusinessTypeAcademias),
		// Spec 084 — comisiones/liquidación a profesionales (peluquería implica;
		// otros tipos lo activan self-service más adelante).
		EnableStaffCommissions: salon,
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
	// Spec 072 — normalización de ubicación. City se deriva del reverse-geocode
	// (Nominatim) y alimenta el cron de scraping por ciudad. LocationReferences =
	// "última milla" ("portón verde, frente a la cancha"). LocationAccuracy en m.
	// Aditivo (Art. X). (0,0) en Latitude/Longitude = sin ubicación.
	City               string  `gorm:"type:varchar(120);default:''" json:"city"`
	LocationReferences string  `gorm:"type:text;default:''" json:"location_references"`
	LocationAccuracy   float64 `gorm:"default:0" json:"location_accuracy"`

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

	// ── Spec 082 — personalización del catálogo online ──────────────────
	// Aditivos (Art. X). Vacíos = el catálogo se ve como hoy.
	//   - StoreTagline: eslogan/descripción corta bajo el nombre.
	//   - BrandColor: color de marca (hex "#RRGGBB") para el tema del catálogo.
	StoreTagline string `gorm:"type:varchar(140);default:''" json:"store_tagline"`
	BrandColor   string `gorm:"type:varchar(9);default:''" json:"brand_color"`
	// StoreHours: horario de atención legible ("Lun–Sáb 8am–8pm"). Se muestra en
	// el catálogo público. Vacío = no se muestra. Spec 082 F2.
	StoreHours string `gorm:"type:varchar(160);default:''" json:"store_hours"`
	// StoreCoverURL: imagen de PORTADA/banner propia del catálogo (la elige el
	// tendero). Vacío = se usa el banner de la plantilla. Spec 082 F2b.
	StoreCoverURL string `gorm:"type:text;default:''" json:"store_cover_url"`
	// CategoryOrder: orden personalizado de las categorías en el catálogo
	// público. Las no listadas van al final (alfabético). Spec 082 F3.
	CategoryOrder []string `gorm:"serializer:json;default:'[]'" json:"category_order"`

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

	// CreditLabelMode controls the vocabulary used in all user-facing strings
	// related to "fiar"/"venta a crédito" (Spec F028). Valid values: "fiar"
	// (default, backward-compatible) and "credit". Internal identifiers
	// (fiado_token, /fiado/<token>, enable_fiados, etc.) are NOT affected.
	CreditLabelMode string `gorm:"type:varchar(10);not null;default:'fiar';check:credit_label_mode IN ('fiar','credit')" json:"credit_label_mode"`

	// ── Spec F029 — precios multi-tier por tipo de cliente ──────────────
	// EnablePriceTiers is the optional capability toggle for multi-tier
	// pricing (depósito mayorista vs cliente final). Default OFF — the
	// 95% of tiendas de barrio that need a single price see no UI change.
	// When ON, products gain 3 optional precio_tier_* columns and the POS
	// shows a tier selector in Confirmar Venta.
	EnablePriceTiers bool `gorm:"not null;default:false" json:"enable_price_tiers"`

	// PriceTier1Name / PriceTier2Name / PriceTier3Name are the per-tenant
	// labels shown in the POS selector and the product edit form. Defaults
	// match the canonical depósito-de-construcción taxonomy; the owner can
	// rename them when activating the capacity (e.g. "Mayorista x12",
	// "Mayorista x6", "Detal"). varchar(50) keeps the UI readable.
	// Explicit `column:` tags pin the snake_case + underscore-before-digit
	// naming GORM's default snake_case converter omits.
	PriceTier1Name string `gorm:"column:price_tier_1_name;type:varchar(50);not null;default:'Depósito contado'" json:"price_tier_1_name"`
	PriceTier2Name string `gorm:"column:price_tier_2_name;type:varchar(50);not null;default:'Depósito crédito'" json:"price_tier_2_name"`
	PriceTier3Name string `gorm:"column:price_tier_3_name;type:varchar(50);not null;default:'Cliente final'"   json:"price_tier_3_name"`

	// Spec F030 — EnableCustomerManagement is the optional capability
	// toggle for identified-clientele businesses (panaderías de pedido,
	// restaurantes, ferreterías, peluquerías). Default OFF: the 95% of
	// tiendas/minimercados whose volume makes per-sale customer capture
	// pure friction see no UI change. When ON, the POS checkout exposes a
	// "Cliente" tile and the main menu shows a "Mis clientes" entry.
	// Additive — every pre-F030 tenant reads false and behaves exactly
	// as before (Constitución Art. X).
	EnableCustomerManagement bool `gorm:"not null;default:false" json:"enable_customer_management"`

	// Spec F031 — EnableQuotes is the optional capability toggle for the
	// quotes module (cotizaciones). Default OFF: the 95% of tiendas /
	// minimercados that sell de contado from a fixed inventory never see
	// the "Cotizaciones" menu entry. When ON, the app exposes the quotes
	// list, the quote form, and the public approval link. Additive —
	// every pre-F031 tenant reads false and behaves exactly as before
	// (Constitución Art. X).
	EnableQuotes bool `gorm:"not null;default:false" json:"enable_quotes"`

	// Spec F033 — EnablePromotions is the optional capability toggle for
	// the customer-broadcast promotions module. Default OFF: the tiendas
	// that don't run segmented WhatsApp campaigns never see the
	// "Promociones" menu entry. When ON, the app exposes the broadcast
	// promotions list, the promo form, the RFM audience selector and the
	// assisted WhatsApp queue. Additive — every pre-F033 tenant reads
	// false and behaves exactly as before (Constitución Art. X). Distinct
	// from the legacy combo-promo module (migraciones 018-019), which is
	// always available and unaffected by this flag.
	EnablePromotions bool `gorm:"not null;default:false" json:"enable_promotions"`

	// Spec F036 — OnboardingCompleted gates the first-run onboarding
	// wizard. Born false for every new registration so the merchant sees
	// the 3-step wizard once; the wizard PATCHes it to true on finish or
	// skip. Pre-F036 tenants are backfilled to true on boot
	// (BackfillOnboardingCompleted) so an established business never sees
	// the wizard. Additive, not-null with a false default — every
	// pre-F036 read is well-defined (Constitución Art. X).
	OnboardingCompleted bool `gorm:"not null;default:false" json:"onboarding_completed"`

	// Spec 098 — aceptación de Términos y Servicios (incluye la cláusula de uso
	// COLABORATIVO de imágenes de producto: las fotos con barcode válido pueden
	// sugerirse a otras tiendas y viceversa). Se captura al registrarse; los
	// tenants previos (versión distinta a CatalogTermsVersion) deben re-aceptar
	// en el próximo ingreso. Aditivo (Art. X): vacío/NULL = no aceptó la versión
	// vigente. El aporte automático (Fase 2) sólo ocurre si aceptó la vigente.
	TermsAcceptedVersion string     `gorm:"type:varchar(32)" json:"terms_accepted_version"`
	TermsAcceptedAt      *time.Time `json:"terms_accepted_at,omitempty"`

	// Spec 104 — suspensión del catálogo público (no del POS). Non-nil =
	// tienda.vendia.store/<slug> y el pedido público responden "no
	// disponible"; el POS y la API autenticada siguen intactos. En F1 se
	// setea solo manualmente (god-mode/SQL); los strikes automáticos son F3.
	CatalogSuspendedAt *time.Time `json:"catalog_suspended_at,omitempty"`

	// Spec F037 — capabilities migrated from byType→optional. F037 reverts
	// F036's auto-activation by business_type: every tenant now arrives with
	// zero optional capabilities and discovers them through the Dashboard
	// reel. To keep PRE-existing tenants from losing access to modules they
	// were already using (recetas, insumos, trabajos de muebles, órdenes de
	// compra, marketing hub), the boot-time backfills flip the matching
	// enable_* flag to true when the tenant has at least one row in the
	// corresponding table. Additive, not-null with false default — every
	// pre-F037 read is well-defined (Constitución Art. X).

	// EnableMarketingHub gates the Marketing Hub bundle (combo-promos +
	// banners + public catalog config) on the Dashboard. Default OFF — the
	// 95% of tiendas that don't run campaigns never see the "Marketing"
	// card. Backfilled to true for tenants with at least one Promotion row.
	EnableMarketingHub bool `gorm:"not null;default:false" json:"enable_marketing_hub"`

	// EnableRecipes gates the Recetas module on the Dashboard. Default OFF.
	// Backfilled to true for tenants with at least one Recipe row.
	EnableRecipes bool `gorm:"not null;default:false" json:"enable_recipes"`

	// EnableSupplies gates the Insumos module on the Dashboard. Default OFF.
	// Backfilled to true for tenants with at least one Ingredient row.
	EnableSupplies bool `gorm:"not null;default:false" json:"enable_supplies"`

	// EnableFurnitureJobs gates the Trabajos de Muebles module on the
	// Dashboard. Default OFF. Backfilled to true for tenants with at least
	// one WorkOrder row.
	EnableFurnitureJobs bool `gorm:"not null;default:false" json:"enable_furniture_jobs"`

	// EnablePurchaseOrders gates the Órdenes de Compra module on the
	// Dashboard. Default OFF. Backfilled to true for tenants with at least
	// one PurchaseOrder row.
	EnablePurchaseOrders bool `gorm:"not null;default:false" json:"enable_purchase_orders"`

	// HideOffersSection oculta la sección de "Ofertas" (promociones) del
	// catálogo público. Default false = visible. Lo controla el switch
	// "Sección de Ofertas visible" del Marketing Hub (antes no persistía).
	HideOffersSection bool `gorm:"not null;default:false" json:"hide_offers_section"`

	// ── Spec 095 — variantes de producto (talla/color/atributos) ────────
	// EnableProductVariants is the optional capability toggle to group
	// products as variants (talla/color) of a shared item. Default OFF —
	// a tienda de barrio or restaurante that never touches this sees zero
	// behavior change. When ON, the tendero can create a
	// ProductVariantGroup and link Products to it via VariantGroupID.
	EnableProductVariants bool `gorm:"not null;default:false" json:"enable_product_variants"`

	// Spec F038 — umbral global de stock crítico por tenant.
	// Cuando un producto cruza este valor hacia abajo en una venta,
	// el dispatcher envía push "Stock bajo" al dueño + cashiers.
	// Nullable para retrocompatibilidad (Art. X): tenants viejos lo
	// leen como NULL → el dispatcher aplica el default StockLowThresholdDefault.
	// El dueño lo cambia desde la pantalla de settings (PATCH /tenants/me).
	StockLowThreshold *int `gorm:"type:int" json:"stock_low_threshold,omitempty"`

	Employees []Employee `gorm:"foreignKey:TenantID" json:"employees,omitempty"`
}

// StockLowThresholdDefault es el valor que se aplica cuando un tenant
// no tiene `StockLowThreshold` configurado (NULL). 3 unidades es lo
// que el tendero típico considera "casi se acaba" — ajustable tras
// observar producción.
const StockLowThresholdDefault = 3

// CountActiveModules cuenta cuántos módulos/capacidades tiene activos el
// tenant: las capacidades opcionales que el dueño prendió (columnas
// enable_*) más los feature flags type-derivados en ON (mesas, KDS,
// servicios, eventos…). Lo usa el god-mode para mostrar "cuántos módulos
// activos" por tenant. No incluye EnableFiados (base, default ON).
// AcceptedCurrentTerms — ¿el tenant aceptó la versión vigente de los términos
// (con la cláusula colaborativa)? Gobierna el aporte automático (Spec 098).
func (t *Tenant) AcceptedCurrentTerms() bool {
	return t.TermsAcceptedVersion == CatalogTermsVersion
}

func (t *Tenant) CountActiveModules() int {
	n := 0
	for _, on := range []bool{
		t.EnablePriceTiers,
		t.EnableCustomerManagement,
		t.EnableQuotes,
		t.EnablePromotions,
		t.EnableMarketingHub,
		t.EnableRecipes,
		t.EnableSupplies,
		t.EnableFurnitureJobs,
		t.EnablePurchaseOrders,
		t.EnableProductVariants,
		t.FeatureFlags.EnableTables,
		t.FeatureFlags.EnableKDS,
		t.FeatureFlags.EnableTips,
		t.FeatureFlags.EnableServices,
		t.FeatureFlags.EnableCustomBilling,
		t.FeatureFlags.EnableFractionalUnits,
		t.FeatureFlags.EnableEvents,
	} {
		if on {
			n++
		}
	}
	return n
}
