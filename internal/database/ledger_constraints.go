package database

import (
	"gorm.io/gorm"
)

// IsPostgres reports whether the active driver is PostgreSQL. SQLite
// (used in unit tests) is the only other dialect this codebase opens,
// and the regexp_replace + partial-index syntax below is Postgres-
// specific, so callers gate on this before issuing the SQL.
func IsPostgres(db *gorm.DB) bool {
	if db == nil || db.Dialector == nil {
		return false
	}
	return db.Dialector.Name() == "postgres"
}

// backfillNormalizedPhones rewrites customers.phone in-place using the
// same digit-only rule as handlers.normalizePhone (strip whitespace,
// dashes, parentheses, plus, leading zeros and the +57 country prefix).
// Done in SQL so we don't have to load every row into Go just to write
// the same value back. MUST run BEFORE applyLedgerIndexes — otherwise
// existing rows with denormalized phones would fail the new unique
// partial index on (tenant_id, phone).
func backfillNormalizedPhones(db *gorm.DB) error {
	stmt := `
		UPDATE customers
		SET phone = regexp_replace(
			regexp_replace(phone, '[\s\-\(\)\+]', '', 'g'),
			'^0+', ''
		)
		WHERE phone IS NOT NULL
		  AND phone <> regexp_replace(
			regexp_replace(phone, '[\s\-\(\)\+]', '', 'g'),
			'^0+', ''
		);
	`
	return db.Exec(stmt).Error
}

// applyLedgerIndexes installs the Postgres-only partial unique indexes
// that back the Ledger Reconstruction epic's invariants. Both
// statements are idempotent (IF NOT EXISTS) so re-running them on a
// boot with existing indexes is a no-op.
//
//   - uq_one_open_account: physically prevents a tenant from holding
//     two simultaneous open/partial/pending ledger accounts for the
//     same customer. Defensive backstop against any future race in
//     the app-level check inside InitFiado.
//   - uq_customer_phone: phone is the stable identity key for a
//     customer; we want at most one row per (tenant, phone). Empty
//     phones are excluded because legitimate walk-ins don't always
//     leave a contact number.
func applyLedgerIndexes(db *gorm.DB) error {
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_one_open_account
		 ON credit_accounts (tenant_id, customer_id)
		 WHERE status IN ('open','partial','pending') AND deleted_at IS NULL`,

		`CREATE UNIQUE INDEX IF NOT EXISTS uq_customer_phone
		 ON customers (tenant_id, phone)
		 WHERE phone <> '' AND deleted_at IS NULL`,
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
