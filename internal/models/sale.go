package models

type PaymentMethod string

const (
	PaymentCash     PaymentMethod = "cash"
	PaymentTransfer PaymentMethod = "transfer"
	PaymentCard     PaymentMethod = "card"
	PaymentCredit   PaymentMethod = "credit"
)

type Sale struct {
	BaseModel

	TenantID  string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	CreatedBy *string `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID  *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Total     float64 `gorm:"not null" json:"total"`
	// TaxAmount is the IVA (or other tax) charged on this sale.
	// Expressed in the same currency as Total. Zero for exempt tenants.
	TaxAmount float64 `gorm:"type:numeric(12,2);not null;default:0" json:"tax_amount"`
	// TipAmount is the optional propina (service/food businesses).
	// Kept separate from Total so reports can filter it out without
	// re-parsing line items.
	TipAmount     float64       `gorm:"type:numeric(12,2);not null;default:0" json:"tip_amount"`
	PaymentMethod PaymentMethod `gorm:"not null;default:'cash'" json:"payment_method"`
	// CustomerID links the sale to an identified Customer (Spec F030).
	// Nullable: most cash sales stay anonymous (customer_id = null) and
	// that is a valid state — association is opt-in. The composite
	// partial index that powers the "Mis clientes" aggregates and the
	// per-customer history timeline is installed in the bootstrap as
	// `idx_sales_customer_created` (database/ledger_constraints.go) —
	// AutoMigrate can't express a Postgres partial index with a DESC
	// column over a BaseModel field.
	CustomerID *string `gorm:"type:uuid;index" json:"customer_id,omitempty"`
	// CustomerNameSnapshot / CustomerPhoneSnapshot freeze the customer's
	// identity at the moment of sale. Reprinting an old receipt must not
	// depend on the Customer row still existing or still matching the
	// name the customer had at the time. Empty strings when there is no
	// customer attached.
	CustomerNameSnapshot  string  `gorm:"type:varchar(128);not null;default:''" json:"customer_name_snapshot,omitempty"`
	CustomerPhoneSnapshot string  `gorm:"type:varchar(32);not null;default:''" json:"customer_phone_snapshot,omitempty"`
	EmployeeUUID          *string `gorm:"type:uuid" json:"employee_uuid,omitempty"`
	EmployeeName          string  `json:"employee_name,omitempty"`
	ReceiptNumber         int64   `gorm:"index" json:"receipt_number,omitempty"`
	IsCredit              bool    `gorm:"default:false" json:"is_credit"`
	// CreditAccountID links this sale to a specific open fiado so the public
	// acceptance page can show itemized detail per debt entry. Nullable for
	// cash/transfer/card sales.
	CreditAccountID *string `gorm:"type:uuid;index" json:"credit_account_id,omitempty"`
	// PaymentStatus supports the zero-fee dynamic-QR flow: COMPLETED is
	// the default for cash + credit sales, transfer-with-QR sales go
	// through PENDING until the cashier manually confirms receipt of the
	// Nequi/Daviplata/Bancolombia transfer.
	PaymentStatus string `gorm:"type:varchar(32);not null;default:'COMPLETED';index" json:"payment_status"`
	// DynamicQRPayload is the raw QR string shown to the customer for
	// transfer sales (e.g. "nequi://pay?phone=300…&amount=12500"). Kept
	// for audit + later reconciliation when Webhooks land.
	DynamicQRPayload *string `gorm:"type:text" json:"dynamic_qr_payload,omitempty"`
	// Source attributes the sale to its origin so the unified ledger
	// can split totals by channel. Defaults to "POS" for the cashier
	// surface; "WEB" lands when an OnlineOrder transitions to
	// completed; "TABLE" when an OrderTicket closes via CloseOrder.
	Source string `gorm:"type:varchar(16);not null;default:'POS';index" json:"source"`
	// ReceiptImageURL points to a Supabase Storage object in the
	// `payment_receipts` bucket. The cashier must attach a photo of
	// the bank notification when paying with a digital method
	// (transfer/QR/credit-app). Empty when payment is cash. The blob
	// is purged automatically after 8 days by a server-side cron;
	// the URL stays here as audit trail of the cashier's action.
	ReceiptImageURL string `gorm:"type:text;default:''" json:"receipt_image_url"`
	// ── Spec F029 — precios multi-tier por tipo de cliente ──────────────
	// PriceTier records WHICH tier was applied to the whole sale. Stored
	// as enum string (no FK) so renaming a tier label later doesn't
	// rewrite historical sales — `tier_N` is the stable identity, the
	// label is presentation. Default 'retail' makes the legacy sale path
	// invisible: every pre-F029 sale is interpreted as a retail sale.
	// CHECK constraint enforces the four valid values at the DB layer.
	PriceTier string `gorm:"type:varchar(10);not null;default:'retail';check:price_tier IN ('retail','tier_1','tier_2','tier_3')" json:"price_tier"`
	// QuoteID links a sale back to the Quote it was converted from
	// (Spec F031 AC-09). Nullable — only the converted-quote path sets
	// it; every regular POS / web / table sale leaves it NULL. The
	// reverse link lives on Quote.SaleID. Convention: *string + UUIDPtr.
	QuoteID *string `gorm:"type:uuid;index" json:"quote_id,omitempty"`
	// CostAmount is a NON-product cost booked directly on the sale, used when
	// there are no SaleItems with a product purchase_price to derive COGS from.
	// Event sales set it to the event's per-attendee cost so the profit formula
	// (revenue − product COGS − cost_amount) works uniformly. Default 0 for
	// every regular POS/web/table sale (their cost comes from product COGS).
	CostAmount float64 `gorm:"type:numeric(12,2);not null;default:0" json:"cost_amount"`
	// EventRegistrationID links a sale created from a confirmed event
	// inscription (Source="EVENT"). Nullable — only event sales set it. It also
	// guarantees idempotency: one confirmed registration ⇒ at most one sale.
	EventRegistrationID *string    `gorm:"type:uuid;index" json:"event_registration_id,omitempty"`
	Items               []SaleItem `gorm:"foreignKey:SaleID" json:"items"`
	// Customer is the optional identified-clientele relation (Spec F030).
	// Populated only when the handler Preloads it; nil for anonymous
	// sales. The receipt-time identity is still frozen in
	// CustomerNameSnapshot / CustomerPhoneSnapshot — this relation is for
	// the live "Mis clientes" views, not for reprinting old receipts.
	Customer *Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
}

// PriceTier enumerates the four valid values for Sale.PriceTier (F029).
const (
	PriceTierRetail = "retail"
	PriceTier1      = "tier_1"
	PriceTier2      = "tier_2"
	PriceTier3      = "tier_3"
)

// IsValidPriceTier returns true when v is one of the four canonical
// price-tier identifiers used by Sale.PriceTier (Spec F029 FR-07).
func IsValidPriceTier(v string) bool {
	switch v {
	case PriceTierRetail, PriceTier1, PriceTier2, PriceTier3:
		return true
	}
	return false
}

// SaleSource enumerates the source vocab for the unified ledger.
const (
	SaleSourcePOS   = "POS"
	SaleSourceWeb   = "WEB"
	SaleSourceTable = "TABLE"
	// SaleSourceEvent attributes a sale to a confirmed event inscription, so the
	// unified ledger and the financial dashboard split it as its own channel.
	SaleSourceEvent = "EVENT"
)

// SaleItem is agnostic as of migration 020: it can represent either a
// product line (physical retail — ProductID populated, IsService=false)
// or a service line (ad-hoc billing — ProductID nil, IsService=true,
// CustomDescription + CustomUnitPrice populated). The database enforces
// the mutual exclusion via the `sale_items_product_or_service` CHECK.
type SaleItem struct {
	BaseModel

	SaleID            string  `gorm:"type:uuid;not null;index" json:"sale_id"`
	ProductID         *string `gorm:"type:uuid" json:"product_id,omitempty"`
	Name              string  `gorm:"not null" json:"name"`
	Price             float64 `gorm:"not null" json:"price"`
	Quantity          int     `gorm:"not null;default:1" json:"quantity"`
	Subtotal          float64 `gorm:"not null" json:"subtotal"`
	IsContainerCharge bool    `gorm:"default:false" json:"is_container_charge"`
	// IsService toggles the line into service-billing mode. When true,
	// ProductID must be nil and CustomDescription must be filled.
	IsService         bool    `gorm:"not null;default:false" json:"is_service"`
	CustomDescription string  `gorm:"type:varchar(256);not null;default:''" json:"custom_description,omitempty"`
	CustomUnitPrice   float64 `gorm:"type:numeric(12,2);not null;default:0" json:"custom_unit_price,omitempty"`
}
