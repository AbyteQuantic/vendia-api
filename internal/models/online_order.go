package models

import "time"

type OnlineOrder struct {
	ID            string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	TenantID      string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	// Phase-6: pin every order to the sede that will fulfill it so
	// the KDS in that branch sees it and stock moves are scoped
	// correctly. Nullable pointer — legacy rows (pre-Phase-5) and
	// mono-sede tenants without a branch row still work.
	BranchID *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	CustomerName  string    `gorm:"not null" json:"customer_name"`
	CustomerPhone string    `gorm:"default:''" json:"customer_phone"`
	// DeliveryType: "pickup" | "delivery" | "mesa" (Spec 083 — pedido desde
	// el QR de una mesa; el cliente está sentado y paga en el local).
	DeliveryType  string    `gorm:"default:'pickup'" json:"delivery_type"`
	// Spec 083 — pedido de mesa. TableID/TableLabel se llenan cuando el pedido
	// entró por el QR de una mesa (?mesa=<id> en el catálogo). El label se
	// snapshot-ea para que la tarea/recibo lo muestre aunque la mesa se renombre
	// o borre. Vacíos para pickup/delivery.
	TableID    *string `gorm:"type:uuid;index" json:"table_id,omitempty"`
	TableLabel string  `gorm:"default:''" json:"table_label,omitempty"`
	// PaymentMethod is the free-form name selected by the customer
	// in the public catalog ("Efectivo", "Nequi Personal", etc.) —
	// the ID of the TenantPaymentMethod row is duplicated into
	// PaymentMethodID when the selection was from a configured
	// payment method, kept as a hint for receipts.
	PaymentMethod   string `gorm:"default:''" json:"payment_method"`
	PaymentMethodID string `gorm:"type:uuid;default:null" json:"payment_method_id,omitempty"`
	Status          string `gorm:"default:'pending'" json:"status"`
	TotalAmount     float64 `gorm:"default:0" json:"total_amount"`
	Items           string  `gorm:"type:jsonb;default:'[]'" json:"items"`
	Notes           string  `gorm:"default:''" json:"notes"`
	// Spec 063 — control de edad. Cuando el pedido incluye un producto de
	// venta restringida a mayores de 18 (licor, cigarrillos), el cliente
	// declara su fecha de nacimiento en el checkout. Se guarda como ISO
	// "yyyy-mm-dd" y AgeConfirmed queda en true solo si el backend verificó
	// que es mayor de edad. Vacío/false para pedidos sin productos +18.
	CustomerBirthDate string `gorm:"default:''" json:"customer_birth_date,omitempty"`
	AgeConfirmed      bool   `gorm:"default:false" json:"age_confirmed"`
}
