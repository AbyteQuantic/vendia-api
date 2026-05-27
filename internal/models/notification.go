// Spec: specs/F38-notifications-deeplink/spec.md
package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NotificationData carries the deep-link payload the client needs
// to navigate from a notification tile straight to the action the
// notification refers to (mesa específica, abono por confirmar,
// pedido web, cuenta de fiado). All fields are optional — older
// rows created before F38 have `{}` and the client falls back to
// the generic list screen for the kind.
//
// Stored as JSONB with the `serializer:json` GORM tag — same
// pattern as `tenants.feature_flags`. Empty/nil maps round-trip as
// `{}` so the column can stay `not null default '{}'`.
//
// IMPORTANT: keep this open (map[string]any). Notification types
// evolve faster than this struct; clients read by key, server
// writes by key. Adding a new key is a non-breaking change.
type NotificationData map[string]any

type Notification struct {
	ID        string           `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time        `json:"created_at"`
	TenantID  string           `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Title     string           `gorm:"not null" json:"title"`
	Body      string           `gorm:"default:''" json:"body"`
	Type      string           `gorm:"default:'info'" json:"type"`
	IsRead    bool             `gorm:"default:false" json:"is_read"`
	Data      NotificationData `gorm:"serializer:json;type:jsonb;not null;default:'{}'" json:"data"`
}

// BeforeCreate fills the id client-side when the storage engine
// won't (SQLite in tests, or Postgres without the pgcrypto
// extension in some self-hosted deployments). No-op when the
// caller or the DB already produced one — keeps this idempotent
// with the `default:gen_random_uuid()` column clause.
func (n *Notification) BeforeCreate(tx *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	if n.Data == nil {
		n.Data = NotificationData{}
	}
	return nil
}
