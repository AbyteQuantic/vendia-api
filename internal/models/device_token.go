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
	// DeviceTokenPlatformWebIOS es la variante de `web` usada cuando
	// el browser es iOS Safari. Lo distinguimos porque el frontend
	// rutea por Web Push API nativo (no FCM JS SDK) — bug irresoluble
	// de firebase_messaging + Flutter web + WebKit (2026-05-29). El
	// backend lo trata como cualquier otro Web Push: si la fila lleva
	// `endpoint+p256dh+auth`, sender envía vía webpush-go; si lleva
	// `token`, vía FCM Admin SDK.
	DeviceTokenPlatformWebIOS = "web_ios"
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
	// Token es el FCM registration token. Vacío cuando el dispositivo
	// usa Web Push protocolo nativo (iOS Safari) — en ese caso van
	// Endpoint/P256dh/Auth abajo. Al menos UNO de los dos modos debe
	// estar lleno (validación en BeforeCreate).
	Token       string  `gorm:"size:512;index:idx_device_token_active,where:invalidated_at IS NULL AND token <> ''" json:"-"`
	Platform    string  `gorm:"not null;size:16" json:"platform"`
	DeviceLabel *string `gorm:"size:120" json:"device_label,omitempty"`

	// Spec 038 — Web Push nativo (RFC 8030/8291). Cuando el browser
	// expone navigator.serviceWorker.pushManager.subscribe() — caso
	// iOS Safari — el cliente registra estos 3 campos en vez de un
	// token FCM. El dispatcher detecta cuál usar viendo si Endpoint
	// está vacío o no. Todos nullable para retrocompatibilidad
	// (Art. X) con dispositivos FCM ya registrados.
	Endpoint *string `gorm:"type:text" json:"-"`
	P256dh   *string `gorm:"type:text" json:"-"`
	Auth     *string `gorm:"type:text" json:"-"`

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
	// Modo 1 — FCM token (web, android). Modo 2 — Web Push nativo
	// (web_ios). Al menos uno debe estar completo.
	hasFCM := strings.TrimSpace(d.Token) != ""
	hasWebPush := d.Endpoint != nil && *d.Endpoint != "" &&
		d.P256dh != nil && *d.P256dh != "" &&
		d.Auth != nil && *d.Auth != ""
	if !hasFCM && !hasWebPush {
		return errors.New(
			"device_token: falta credencial — necesita token (FCM) o " +
				"endpoint+p256dh+auth (Web Push)")
	}
	switch d.Platform {
	case DeviceTokenPlatformWeb, DeviceTokenPlatformAndroid, DeviceTokenPlatformWebIOS:
		return nil
	default:
		return errors.New("device_token: platform inválida (esperado web|web_ios|android)")
	}
}

// IsWebPush retorna true si la fila lleva credenciales Web Push
// nativas (RFC 8291). Lo usa el sender para rutear: si true → vía
// webpush-go con VAPID; si false → vía FCM Admin SDK con el Token.
func (d *DeviceToken) IsWebPush() bool {
	return d.Endpoint != nil && *d.Endpoint != "" &&
		d.P256dh != nil && *d.P256dh != "" &&
		d.Auth != nil && *d.Auth != ""
}
