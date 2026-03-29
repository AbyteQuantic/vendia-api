package models

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
	Label           string      `gorm:"not null" json:"label"`
	CustomerName    string      `json:"customer_name,omitempty"`
	EmployeeUUID    string      `gorm:"type:uuid" json:"employee_uuid,omitempty"`
	EmployeeName    string      `json:"employee_name,omitempty"`
	Status          OrderStatus `gorm:"not null;default:'nuevo'" json:"status"`
	Type            OrderType   `gorm:"not null;default:'mesa'" json:"type"`
	Total           float64     `gorm:"default:0" json:"total"`
	DeliveryAddress string      `json:"delivery_address,omitempty"`
	CustomerPhone   string      `json:"customer_phone,omitempty"`
	PaymentMethod   string      `json:"payment_method,omitempty"`
	Items           []OrderItem `gorm:"foreignKey:OrderUUID;references:ID" json:"items"`
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
