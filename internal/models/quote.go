// Spec: specs/031-cotizaciones/spec.md
package models

import "time"

// Quote status vocabulary (Spec F031 AC-05). The FSM that governs the
// allowed transitions between these states lives in
// services/quote_state.go — keep both in sync.
const (
	// QuoteStatusDraft — recién creada, editable, aún no enviada.
	QuoteStatusDraft = "borrador"
	// QuoteStatusSent — enviada al cliente; espera su decisión.
	QuoteStatusSent = "enviada"
	// QuoteStatusApproved — el cliente aprobó; lista para convertir en venta.
	QuoteStatusApproved = "aprobada"
	// QuoteStatusRejected — el cliente rechazó.
	QuoteStatusRejected = "rechazada"
	// QuoteStatusExpired — pasó su valid_until sin respuesta.
	QuoteStatusExpired = "vencida"
	// QuoteStatusConverted — convertida en venta; guarda el sale_id.
	QuoteStatusConverted = "convertida"
	// QuoteStatusReplaced — fue editada estando enviada; reemplazada por una v2.
	QuoteStatusReplaced = "reemplazada"
)

// ValidQuoteStatuses is the canonical whitelist used by the CHECK
// constraint and by handler-layer validation (defense in depth — the
// app rejects an invalid status with a Spanish message before the DB
// CHECK would throw a 500).
var ValidQuoteStatuses = map[string]struct{}{
	QuoteStatusDraft:     {},
	QuoteStatusSent:      {},
	QuoteStatusApproved:  {},
	QuoteStatusRejected:  {},
	QuoteStatusExpired:   {},
	QuoteStatusConverted: {},
	QuoteStatusReplaced:  {},
}

// IsValidQuoteStatus reports whether s is one of the seven canonical
// quote states (Spec F031 AC-05).
func IsValidQuoteStatus(s string) bool {
	_, ok := ValidQuoteStatuses[s]
	return ok
}

// Quote is a price proposal a merchant sends to an identified customer
// (Spec F031). It is NOT a definitive document — unlike a Sale it can be
// edited, expire, or be rejected. When approved it can be converted into
// a Sale (the SaleID link is the audit trail of that conversion).
//
// Multi-tenant invariants (Constitución Art. III):
//   - Every read/write is scoped by TenantID from the JWT (private CRUD)
//     or resolved from the unguessable PublicToken (public endpoint).
//   - CustomerID is NOT NULL: a quote always names a customer (Spec §4 —
//     cliente obligatorio, reusa el selector de F030).
//
// Folio format COT-YYYY-NNNN — assigned atomically from QuoteSequence on
// creation. A quote edited while `enviada` produces a v2 with the same
// folio plus a `-V2` suffix; the v1 row is marked `reemplazada` and
// points at the v2 via ReplacedByID.
type Quote struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	// CustomerID links the quote to an identified Customer (Spec F030).
	// NOT NULL — a quote always names a customer.
	CustomerID string    `gorm:"type:uuid;not null;index" json:"customer_id"`
	Customer   *Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`

	// Folio is the human-facing identifier, e.g. COT-2026-0001 (or
	// COT-2026-0001-V2 for an edited-while-sent revision).
	Folio string `gorm:"type:varchar(24);not null" json:"folio"`
	// Status is one of ValidQuoteStatuses. The CHECK constraint enforces
	// the whitelist at the DB layer.
	Status string `gorm:"type:varchar(20);not null;default:'borrador';check:quote_status_check,status IN ('borrador','enviada','aprobada','rechazada','vencida','convertida','reemplazada')" json:"status"`

	// ValidUntil is the expiry date. Past it without a decision, the
	// quote moves to `vencida` (cron job + lazy check on the public read).
	ValidUntil time.Time `gorm:"not null" json:"valid_until"`
	Note       string    `gorm:"type:text" json:"note"`

	// Pricing fields. All in COP (Constitución — multimoneda fuera de
	// scope). Computed by services.ComputeQuoteTotals from the items.
	DiscountTotal float64 `gorm:"type:numeric(12,2);not null;default:0" json:"discount_total"`
	TaxRate       float64 `gorm:"type:numeric(6,4);not null;default:0" json:"tax_rate"`
	Subtotal      float64 `gorm:"type:numeric(12,2);not null;default:0" json:"subtotal"`
	TaxAmount     float64 `gorm:"type:numeric(12,2);not null;default:0" json:"tax_amount"`
	Total         float64 `gorm:"type:numeric(12,2);not null;default:0" json:"total"`

	// PublicToken is the unguessable UUID that powers the public approval
	// link tienda.vendia.store/c/<token>. Generated server-side on
	// creation (≥122 bits entropy — resists enumeration, Spec plan D4).
	PublicToken string `gorm:"type:uuid;not null;uniqueIndex" json:"public_token"`

	// ReplacedByID points at the v2 quote when this row was superseded by
	// an edit-while-sent. Nullable — convention is *string + UUIDPtr.
	ReplacedByID *string `gorm:"type:uuid" json:"replaced_by_id,omitempty"`
	// SaleID links to the Sale created when this quote was converted.
	// Nullable — only set once Status == convertida.
	SaleID *string `gorm:"type:uuid" json:"sale_id,omitempty"`

	// SentAt / DecidedAt / DecidedByIP are the lightweight audit trail of
	// the send + customer-decision lifecycle (Spec §6 — firma digital
	// fuera de scope; IP + timestamp queda como evidencia básica).
	SentAt      *time.Time `json:"sent_at,omitempty"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	DecidedByIP string     `gorm:"type:varchar(45)" json:"decided_by_ip,omitempty"`

	Items []QuoteItem `gorm:"foreignKey:QuoteID" json:"items"`
}

// QuoteItem is one line of a quote. Like SaleItem it is agnostic: a line
// can reference a catalogue product (ProductID populated) or be a free
// line (ProductID nil — e.g. "Mano de obra"). The handler validates that
// a product line belongs to the quote's tenant.
type QuoteItem struct {
	BaseModel

	QuoteID string `gorm:"type:uuid;not null;index" json:"quote_id"`
	// ProductID is nil for a free line. When set it must reference a
	// product owned by the quote's tenant.
	ProductID *string `gorm:"type:uuid" json:"product_id,omitempty"`
	Name      string  `gorm:"type:varchar(200);not null" json:"name"`
	Quantity  float64 `gorm:"type:numeric(12,3);not null" json:"quantity"`
	UnitPrice float64 `gorm:"type:numeric(12,2);not null" json:"unit_price"`
	// Discount is the per-line discount in COP (absolute, not a rate).
	Discount float64 `gorm:"type:numeric(12,2);not null;default:0" json:"discount"`
	// Subtotal == (Quantity * UnitPrice) - Discount, computed by
	// services.ComputeQuoteTotals.
	Subtotal  float64 `gorm:"type:numeric(12,2);not null;default:0" json:"subtotal"`
	SortOrder int     `gorm:"not null;default:0" json:"sort_order"`
}

// QuoteSequence is the per-tenant, per-year folio counter. NextValue is
// the next number to hand out. The atomic increment (transaction +
// SELECT FOR UPDATE) lives in services.NextQuoteFolio — see Spec plan D2
// for why a manual counter table beats a Postgres SEQUENCE here.
//
// Composite primary key (TenantID, Year) — no BaseModel, this is a pure
// counter row with no soft-delete semantics.
type QuoteSequence struct {
	TenantID  string    `gorm:"type:uuid;primaryKey" json:"tenant_id"`
	Year      int       `gorm:"primaryKey" json:"year"`
	NextValue int       `gorm:"not null;default:1" json:"next_value"`
	UpdatedAt time.Time `json:"updated_at"`
}
