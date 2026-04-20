package models

import "time"

// Valid business type values.
const (
	BusinessTypeTiendaBarrio = "tienda_barrio"
	BusinessTypeMinimercado  = "minimercado"
	BusinessTypeBar          = "bar"
	BusinessTypeMiscelanea   = "miscelanea"
	BusinessTypeMuebles      = "muebles"
	BusinessTypeManufactura  = "manufactura"
	BusinessTypeReparacion   = "reparacion"
	BusinessTypeComidas      = "comidas_rapidas"
)

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
	RazonSocial  string       `gorm:"not null;default:''" json:"razon_social"`
	NIT          string       `gorm:"not null;default:''" json:"nit"`
	Address      string       `gorm:"not null;default:''" json:"address"`

	SaleTypes    []string `gorm:"serializer:json;not null" json:"sale_types"`
	HasShowcases bool     `gorm:"not null;default:false" json:"has_showcases"`
	HasTables    bool     `gorm:"not null;default:false" json:"has_tables"`

	ChargeMode    string  `gorm:"default:'pre_payment'" json:"charge_mode"`
	EnableFiados  bool    `gorm:"default:true" json:"enable_fiados"`
	DefaultMargin float64 `gorm:"default:20" json:"default_margin"`
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

	SubscriptionStatus string     `gorm:"default:'trial'" json:"subscription_status"`
	SubscriptionEndsAt *time.Time `json:"subscription_ends_at"`

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

	Employees []Employee `gorm:"foreignKey:TenantID" json:"employees,omitempty"`
}
