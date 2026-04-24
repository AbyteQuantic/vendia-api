package models

// PartialPayment — Abono registrado contra una cuenta abierta
// (OrderTicket). Nace de tres flujos:
//
//  1. Cliente en la vista pública de la mesa toca "Pagar / Hacer
//     Abono" y nos manda el monto + método. Queda en PENDING hasta
//     que el tendero lo confirme (el cliente pudo haber tocado
//     "ya pagué" pero el dinero no entró).
//  2. Tendero en el POS registra un abono manual (Ej: el cliente
//     le da un billete de 20.000 en efectivo). Se crea directo
//     como APPROVED.
//  3. Futuro: webhook de un PSP (Wompi, etc.) marca el PENDING
//     como APPROVED al confirmar el cobro.
//
// remaining_balance se calcula siempre en el handler como
// Total - SUM(amount APPROVED). Guardarlo en columna llevaría a
// deriva si algún flujo olvida actualizarlo.
type PartialPayment struct {
	BaseModel

	// OrderID tracks the OrderTicket (cuenta abierta) that this
	// abono pays against. Always required — a payment without an
	// order has no semantic home.
	OrderID string `gorm:"type:uuid;not null;index" json:"order_id"`

	// TenantID denormalises the tenant for fast `?tenant_id=`
	// filtering on reports. Always equals OrderTicket.TenantID
	// of the parent row; enforced in the handler on create.
	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`

	// BranchID mirrors the parent order's sede so Phase-6 list
	// filters cover abonos too.
	BranchID *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`

	// Amount in COP. Always positive — a negative abono is
	// conceptually a refund and needs a different vocabulary.
	Amount float64 `gorm:"type:numeric(12,2);not null" json:"amount"`

	// PaymentMethod is the free-form label the customer chose
	// ("Efectivo", "Nequi Personal", "Daviplata"). Keeping it
	// free-form lets the abono ride through without a tight
	// coupling to TenantPaymentMethod ids; the id (when present)
	// is kept as a hint for reports.
	PaymentMethod   string `gorm:"default:''" json:"payment_method"`
	PaymentMethodID string `gorm:"type:uuid;default:null" json:"payment_method_id,omitempty"`

	// Status transitions: PENDING → APPROVED → (terminal). A
	// REJECTED state is intentionally absent — if the tendero
	// decides the abono didn't land, they delete the row (soft
	// delete via BaseModel). Keeps the state machine flat.
	Status string `gorm:"not null;default:'PENDING'" json:"status"`

	// Notes is an optional free-text blurb from the party that
	// created the abono ("pagó por transferencia", "me dio dos
	// billetes de 20"). Kept short in UI, but we don't cap at
	// the DB level.
	Notes string `gorm:"default:''" json:"notes,omitempty"`

	// CreatedByEmployee points at the employee that registered
	// the abono via the POS. Null when the abono was self-filed
	// by the customer through the public live-tab page.
	CreatedByEmployee *string `gorm:"type:uuid;index" json:"created_by_employee,omitempty"`

	// ReceiptURL is the public URL of the customer-submitted
	// transfer screenshot when the abono comes from the live-tab
	// page. The tendero opens it from TabReviewScreen to verify
	// the transfer landed before flipping the abono to APPROVED.
	// Empty for cash abonos and for tendero-registered manual
	// abonos (those land APPROVED with no proof needed).
	ReceiptURL string `gorm:"default:''" json:"receipt_url,omitempty"`
}

// PartialPaymentStatus enumerates the valid states for the state
// machine. Keeping them as typed constants so handlers and tests
// share a single source of truth for whitelisting.
const (
	PartialPaymentStatusPending  = "PENDING"
	PartialPaymentStatusApproved = "APPROVED"
)
