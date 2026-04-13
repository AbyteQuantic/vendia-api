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

	NequiPhone     *string `gorm:"size:15" json:"nequi_phone"`
	DaviplataPhone *string `gorm:"size:15" json:"daviplata_phone"`

	LastSyncAt     *time.Time `json:"last_sync_at"`
	PendingSyncOps int        `gorm:"default:0" json:"pending_sync_ops"`

	SubscriptionStatus string     `gorm:"default:'trial'" json:"subscription_status"`
	SubscriptionEndsAt *time.Time `json:"subscription_ends_at"`

	// Store / Delivery
	StoreSlug      *string `gorm:"uniqueIndex" json:"store_slug,omitempty"`
	IsDeliveryOpen bool    `gorm:"default:false" json:"is_delivery_open"`
	DeliveryCost   float64 `gorm:"default:0" json:"delivery_cost"`
	MinOrderAmount float64 `gorm:"default:0" json:"min_order_amount"`
	LogoURL        string  `json:"logo_url,omitempty"`

	Employees []Employee `gorm:"foreignKey:TenantID" json:"employees,omitempty"`
}
