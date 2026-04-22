package database

import (
	"errors"
	"log"
	"strings"

	"vendia-backend/internal/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// BootstrapSuperAdminConfig carries the seed credentials read from
// env vars. Keeping the dependencies flat (not importing config)
// means this package can be unit-tested without pulling in dotenv
// or touching process state.
type BootstrapSuperAdminConfig struct {
	Email    string
	Password string
	Name     string // optional; defaults to the email local part
}

// BootstrapSuperAdmin upserts a row in `admin_users` when the seed
// env vars are populated. Idempotent by design — running on every
// boot never resets the password on an existing admin (the INSERT
// short-circuits via `ON CONFLICT DO NOTHING` on email).
//
// Intended use: run once right after migrations in cmd/server/main.
// Returns nil when either env var is missing (we don't want to
// silently refuse production boots because someone forgot the seed).
func BootstrapSuperAdmin(db *gorm.DB, cfg BootstrapSuperAdminConfig) error {
	email := strings.ToLower(strings.TrimSpace(cfg.Email))
	pw := cfg.Password
	if email == "" || pw == "" {
		log.Println("[BOOTSTRAP] SEED_ADMIN_EMAIL / SEED_ADMIN_PASSWORD not set — skipping super-admin seed")
		return nil
	}

	// If a row already exists for this email, we don't touch it.
	// Password rotation is intentionally NOT handled via env vars —
	// rotating means PATCH /admin/users/:id with a new bcrypt via a
	// super-admin session, not a server restart.
	var existing models.AdminUser
	err := db.Where("email = ?", email).First(&existing).Error
	if err == nil {
		log.Printf("[BOOTSTRAP] super-admin %q already exists — skipping seed", email)
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = strings.SplitN(email, "@", 2)[0]
	}

	row := models.AdminUser{
		Email:        email,
		PasswordHash: string(hash),
		Name:         name,
		IsSuperAdmin: true,
	}
	if err := db.Create(&row).Error; err != nil {
		return err
	}
	log.Printf("[BOOTSTRAP] super-admin %q seeded (id=%s)", email, row.ID)
	return nil
}
