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
