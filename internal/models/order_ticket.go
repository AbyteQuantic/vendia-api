package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type OrderStatus string

const (
	OrderStatusNuevo     OrderStatus = "nuevo"
	OrderStatusPreparando OrderStatus = "preparando"
	OrderStatusListo     OrderStatus = "listo"
	OrderStatusCobrado   OrderStatus = "cobrado"
	OrderStatusCancelado OrderStatus = "cancelado"
)

type OrderType string

const (
	OrderTypeMesa        OrderType = "mesa"
	OrderTypeTurno       OrderType = "turno"
	OrderTypeParaLlevar  OrderType = "para_llevar"
	OrderTypeDomicilioWeb OrderType = "domicilio_web"
)

type OrderTicket struct {
	BaseModel

	TenantID        string      `gorm:"type:uuid;not null;index" json:"tenant_id"`
	CreatedBy       *string     `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID        *string     `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Label           string      `gorm:"not null" json:"label"`
	CustomerName    string      `json:"customer_name,omitempty"`
	EmployeeUUID    *string     `gorm:"type:uuid" json:"employee_uuid,omitempty"`
	EmployeeName    string      `json:"employee_name,omitempty"`
	Status          OrderStatus `gorm:"not null;default:'nuevo'" json:"status"`
	Type            OrderType   `gorm:"not null;default:'mesa'" json:"type"`
	Total           float64     `gorm:"default:0" json:"total"`
	DeliveryAddress string      `json:"delivery_address,omitempty"`
	CustomerPhone   string      `json:"customer_phone,omitempty"`
	PaymentMethod   string      `json:"payment_method,omitempty"`
	// SessionToken is an opaque, non-guessable UUID that lets a
	// customer scan the table QR and watch their live tab from
	// the public catalog without exposing the tenant_id or the
	// order primary key (which is also a UUID but is used across
	// authenticated endpoints and therefore higher-value to leak).
	// Generated on first write via BeforeCreate and kept stable
	// for the life of the ticket. Unique to prevent collisions.
	SessionToken string `gorm:"type:uuid;uniqueIndex" json:"session_token,omitempty"`
	// WaiterCalledAt marks the last time a customer pressed
	// "Llamar al mesero" from the live-tab page. We store the
	// timestamp (instead of a boolean) so the KDS can show "hace
	// 2 min" and rate-limit repeated calls. Nullable because the
	// vast majority of tickets never trigger the affordance.
	WaiterCalledAt *time.Time  `json:"waiter_called_at,omitempty"`
	Items          []OrderItem `gorm:"foreignKey:OrderUUID;references:ID" json:"items"`
}

// BeforeCreate runs after BaseModel.BeforeCreate (same hook name,
// same receiver pattern). Gorm resolves promoted methods so we
// explicitly override and forward to keep the UUID generation AND
// initialize the session token. Idempotent: if the caller already
// supplied a SessionToken (e.g. restoring from an offline queue),
// we respect it.
func (o *OrderTicket) BeforeCreate(tx *gorm.DB) error {
	if err := o.BaseModel.BeforeCreate(tx); err != nil {
		return err
	}
	if o.SessionToken == "" {
		o.SessionToken = uuid.NewString()
	}
	return nil
}

type OrderItem struct {
	BaseModel

	OrderUUID   string  `gorm:"type:uuid;not null;index" json:"order_uuid"`
	ProductUUID string  `gorm:"type:uuid;not null" json:"product_uuid"`
	ProductName string  `gorm:"not null" json:"product_name"`
	Quantity    int     `gorm:"not null" json:"quantity"`
	UnitPrice   float64 `gorm:"not null" json:"unit_price"`
	Emoji       string  `json:"emoji,omitempty"`
}
