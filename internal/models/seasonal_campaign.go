// Spec: specs/086-branding-estacional/spec.md
package models

import "time"

// IconVariants — whitelist cerrada de sets de íconos PRE-EMPAQUETADOS en el
// binario nativo. icon_variant es una CLAVE, nunca una URL (Apple/Android no
// permiten ícono 100% remoto).
var IconVariants = map[string]struct{}{
	"default":      {},
	"navidad":      {},
	"amor_amistad": {},
	"halloween":    {},
	"patrias":      {},
	"anio_nuevo":   {},
}

// SeasonalCampaign — branding estacional de la PLATAFORMA VendIA (GLOBAL, sin
// tenant_id). Una campaña activa "viste" la app por temporada sin force-update.
// Todos los overrides de marca son nullable (*string) para distinguir
// "no-override" de vacío. Aditivo / AutoMigrate-safe.
type SeasonalCampaign struct {
	BaseModel

	Key         string `gorm:"type:varchar(40);uniqueIndex;not null" json:"key"`
	Name        string `gorm:"type:varchar(120);not null;default:''" json:"name"`
	Enabled     bool   `gorm:"not null;default:false" json:"enabled"`        // kill-switch maestro
	ForceActive bool   `gorm:"not null;default:false" json:"force_active"`   // ignora fechas (QA/lanzamiento)
	Priority    int    `gorm:"not null;default:0" json:"priority"`           // desempate si solapan

	StartsAt *time.Time `gorm:"index" json:"starts_at,omitempty"` // null = sin límite inferior
	EndsAt   *time.Time `gorm:"index" json:"ends_at,omitempty"`   // EXCLUSIVO: now < EndsAt

	// Overrides de marca (nullable = no-override).
	AccentHex      *string `gorm:"type:varchar(7)" json:"accent_hex,omitempty"`
	SplashBgHex    *string `gorm:"type:varchar(7)" json:"splash_bg_hex,omitempty"`
	SplashImageURL *string `gorm:"type:varchar(500)" json:"splash_image_url,omitempty"`
	SplashMessage  *string `gorm:"type:varchar(160)" json:"splash_message,omitempty"`
	BannerText     *string `gorm:"type:varchar(200)" json:"banner_text,omitempty"`
	BannerImageURL *string `gorm:"type:varchar(500)" json:"banner_image_url,omitempty"`
	BannerBgHex    *string `gorm:"type:varchar(7)" json:"banner_bg_hex,omitempty"`
	BannerLinkURL  *string `gorm:"type:varchar(500)" json:"banner_link_url,omitempty"`

	// IconVariant — clave del set de íconos nativo (whitelist IconVariants).
	IconVariant string `gorm:"type:varchar(40);not null;default:'default'" json:"icon_variant"`
}
