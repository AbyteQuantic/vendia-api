// Spec: specs/038-push-notifications-web-android/spec.md
package models

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// DeviceTokenPlatform restringe los valores que aceptamos en Fase 1
// (Web + Android). Fase 2 introducirá `ios` cuando se contrate Apple
// Developer; agregarlo entonces requiere actualizar este enum + el
// validador del modelo.
const (
	DeviceTokenPlatformWeb     = "web"
	DeviceTokenPlatformAndroid = "android"
)

// DeviceToken es un token FCM registrado por un usuario para uno de
// sus dispositivos. La regla maestra (spec § 7): un mismo token nunca
// pertenece a dos tenants a la vez — si el dispositivo cambia de
// tenant (caso empleado que rota de negocio), el row viejo se
// invalida y se crea uno nuevo bajo el nuevo tenant.
//
// El índice único parcial `(tenant_id, token) WHERE invalidated_at IS
// NULL` impide duplicados activos. SQLite no soporta el WHERE en el
// índice, por lo que en tests usamos el GORM tag `uniqueIndex` (sin
// WHERE); el constraint real con WHERE aplica en Postgres prod.
type DeviceToken struct {
	BaseModel

	TenantID    string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	UserID      string  `gorm:"type:uuid;not null;index" json:"user_id"`
	Token       string  `gorm:"not null;uniqueIndex:idx_device_token_active,where:invalidated_at IS NULL" json:"-"`
	Platform    string  `gorm:"not null;size:16" json:"platform"`
	DeviceLabel *string `gorm:"size:120" json:"device_label,omitempty"`

	LastSeenAt    time.Time  `gorm:"not null" json:"last_seen_at"`
	InvalidatedAt *time.Time `json:"invalidated_at,omitempty"`
}

// BeforeCreate hereda BaseModel para la generación de UUID y agrega
// validaciones de defensa en profundidad (los handlers son la primera
// línea de validación, esto es la segunda).
func (d *DeviceToken) BeforeCreate(tx *gorm.DB) error {
	if err := d.BaseModel.BeforeCreate(tx); err != nil {
		return err
	}
	return d.validate()
}

// BeforeUpdate NO valida el token: los UPDATEs parciales del
// dispatcher (p. ej. setear `invalidated_at`) llegan con un struct
// vacío salvo el campo que se está cambiando, y reactivar la
// validación rechazaría esos updates legítimos. El token es
// inmutable por diseño (no se "renombra" un token; si cambia, se
// invalida el viejo y se crea uno nuevo via RegisterDevice), así
// que validar en BeforeCreate cubre el caso real.

func (d *DeviceToken) validate() error {
	if strings.TrimSpace(d.Token) == "" {
		return errors.New("device_token: token vacío no permitido")
	}
	switch d.Platform {
	case DeviceTokenPlatformWeb, DeviceTokenPlatformAndroid:
		return nil
	default:
		return errors.New("device_token: platform inválida (esperado web|android)")
	}
}
