// Spec: specs/057-panic-button-delivery/spec.md
package models

import "time"

// SosAlert es el registro histórico de cada activación del Botón de
// Pánico. Antes el trigger solo logueaba a stdout; ahora queda
// persistido con el estado de entrega por contacto para que el tendero
// vea, en la pantalla de Seguridad, qué alertas se dispararon y si
// llegaron.
type SosAlert struct {
	BaseModel
	TenantID     string             `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Message      string             `gorm:"type:text" json:"message"`
	Latitude     float64            `json:"latitude"`
	Longitude    float64            `json:"longitude"`
	ContactCount int                `json:"contact_count"`
	TriggeredAt  time.Time          `json:"triggered_at"`
	Deliveries   []SosAlertDelivery `gorm:"foreignKey:AlertID" json:"deliveries,omitempty"`
}

// SosAlertDelivery es el intento de entrega a UN contacto, con su
// estado y (si el proveedor respondió) el id del mensaje o el error.
//
// Status: pending | sent | failed | skipped.
//   - skipped = el canal (SMS/WhatsApp) no está configurado en el server
//     (fail-closed): la estructura existe pero faltan credenciales.
type SosAlertDelivery struct {
	BaseModel
	AlertID       string  `gorm:"type:uuid;index;not null" json:"alert_id"`
	TenantID      string  `gorm:"type:uuid;index;not null" json:"tenant_id"`
	ContactName   string  `json:"contact_name"`
	PhoneNumber   string  `json:"phone_number"`
	Method        string  `json:"method"` // sms | whatsapp
	Status        string  `json:"status"`
	ProviderMsgID *string `json:"provider_msg_id,omitempty"`
	ErrorDetail   *string `json:"error_detail,omitempty"`
}
