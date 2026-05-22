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

		// Spec F030 — idx_sales_customer_created backs the "Mis clientes"
		// per-customer aggregates (total_spent, purchase_count,
		// last_purchase_at) and the customer-history timeline. created_at
		// DESC matches the chronological order both queries read in, so
		// the index serves the ORDER BY without a sort step. customer_id
		// is the leading column so anonymous sales (customer_id NULL) are
		// not indexed — keeps it lean for the typical tienda whose sales
		// are mostly anonymous.
		`CREATE INDEX IF NOT EXISTS idx_sales_customer_created
		 ON sales (customer_id, created_at DESC)
		 WHERE customer_id IS NOT NULL AND deleted_at IS NULL`,

		// Spec F031 — idx_quotes_tenant_status_created backs the
		// "Mis cotizaciones" list endpoint, whose default read is
		// tenant-scoped, optionally status-filtered, ordered by
		// created_at DESC. tenant_id leads (every read filters on it),
		// status second (the FilterChips), created_at DESC last so the
		// ORDER BY is served without a sort step. Partial on
		// deleted_at IS NULL keeps soft-deleted drafts out of the index.
		`CREATE INDEX IF NOT EXISTS idx_quotes_tenant_status_created
		 ON quotes (tenant_id, status, created_at DESC)
		 WHERE deleted_at IS NULL`,

		// Spec F031 — the expire-quotes cron scans for sent quotes past
		// their valid_until. A partial index on exactly that predicate
		// keeps the hourly job O(matching rows) instead of a full scan.
		`CREATE INDEX IF NOT EXISTS idx_quotes_expiry_scan
		 ON quotes (valid_until)
		 WHERE status = 'enviada' AND deleted_at IS NULL`,

		// Spec F033 — the promotions-push cron scans for scheduled
		// broadcast promotions whose send time has arrived and that have
		// not been pushed yet. A partial index on exactly that predicate
		// keeps the 5-minute job O(matching rows) instead of a full scan.
		`CREATE INDEX IF NOT EXISTS idx_broadcast_promotions_push_scan
		 ON broadcast_promotions (scheduled_for)
		 WHERE scheduled_for IS NOT NULL AND schedule_push_sent = false
		   AND deleted_at IS NULL`,
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
