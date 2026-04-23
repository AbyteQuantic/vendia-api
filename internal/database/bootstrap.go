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

// BackfillBranchIDs assigns a default branch_id to every legacy
// operational row where the column is still NULL. Phase-6 introduced
// `WHERE branch_id = ?` on products/sales/credits, which would have
// hidden pre-Phase-5 rows from the app. Migration 026 carried the SQL
// backfill, but Render deploys run GORM AutoMigrate only (not goose),
// so the SQL file never fires in production. This function mirrors
// those UPDATE statements in Go and is wired into main.go right after
// AutoMigrate, which means every boot self-heals:
//
//   - The "oldest active branch per tenant" is picked as the default —
//     same tie-breaker as the SQL migration.
//   - credit_payments has no tenant_id column, so it inherits the
//     branch_id from its parent credit_account.
//   - All updates are gated by `branch_id IS NULL` so the function is
//     idempotent. Running it on a fully scoped database is a no-op.
//
// Errors are logged but don't abort the boot — a stranded NULL row is
// preferable to a crashing deploy.
func BackfillBranchIDs(db *gorm.DB) {
	tenantScoped := []string{"products", "sales", "credit_accounts", "order_tickets"}
	for _, tbl := range tenantScoped {
		res := db.Exec(`
			UPDATE `+tbl+` AS t
			   SET branch_id = sub.id
			  FROM (
			      SELECT DISTINCT ON (tenant_id) tenant_id, id
			        FROM branches
			       WHERE deleted_at IS NULL
			       ORDER BY tenant_id, created_at ASC
			  ) sub
			 WHERE t.tenant_id = sub.tenant_id
			   AND t.branch_id IS NULL
			   AND t.deleted_at IS NULL`)
		if res.Error != nil {
			log.Printf("[BOOTSTRAP] backfill %s skipped: %v", tbl, res.Error)
			continue
		}
		if res.RowsAffected > 0 {
			log.Printf("[BOOTSTRAP] backfilled %d rows in %s", res.RowsAffected, tbl)
		}
	}

	res := db.Exec(`
		UPDATE credit_payments cp
		   SET branch_id = ca.branch_id
		  FROM credit_accounts ca
		 WHERE cp.credit_account_id = ca.id
		   AND cp.branch_id IS NULL
		   AND ca.branch_id IS NOT NULL
		   AND cp.deleted_at IS NULL`)
	if res.Error != nil {
		log.Printf("[BOOTSTRAP] backfill credit_payments skipped: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("[BOOTSTRAP] backfilled %d rows in credit_payments", res.RowsAffected)
	}
}
