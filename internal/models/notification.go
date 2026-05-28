package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Notification struct {
	ID        string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	TenantID  string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	Title     string    `gorm:"not null" json:"title"`
	Body      string    `gorm:"default:''" json:"body"`
	Type      string    `gorm:"default:'info'" json:"type"`
	IsRead    bool      `gorm:"default:false" json:"is_read"`

	// Spec 038 — Push Notifications Fase 1. Campos opcionales que
	// permiten que el push del OS y la entrada in-app compartan
	// destino (DeepLink), que el sender lleve la cuenta del cap
	// diario sin tabla aparte (PushedAt), y que el dispatcher
	// deduplique reintentos dentro de la ventana de 5 min (DedupKey).
	// Los 3 son punteros nullable → 100% retrocompatible (Art. X):
	// filas antiguas leen sin error, clientes viejos siguen creando
	// sin estos campos.
	DeepLink *string    `gorm:"type:text" json:"deep_link,omitempty"`
	PushedAt *time.Time `gorm:"index" json:"pushed_at,omitempty"`
	DedupKey *string    `gorm:"type:varchar(120);index" json:"dedup_key,omitempty"`
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
	return nil
}
