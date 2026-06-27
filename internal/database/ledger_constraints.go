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

// applyPerformanceIndexes installs composite indexes that back the hottest
// read paths (dashboard analytics, /tasks poll, product list, offline sync).
// Postgres-only, idempotent (IF NOT EXISTS). The engine today only has
// single-column indexes and filters the rest in memory; these composites let
// the leading-equality + range/order be served from the index. Multi-sede safe:
// branch_id is filtered separately (often `branch_id = ? OR branch_id IS NULL`
// for globals), so we deliberately do NOT lead with branch_id — the equality
// would not serve the OR cleanly. Audit 2026-06-24.
func applyPerformanceIndexes(db *gorm.DB) error {
	statements := []string{
		// Toda la analítica/dashboard y ListSales filtran tenant_id + ordenan
		// por created_at DESC. Partial deleted_at respeta el soft-delete.
		`CREATE INDEX IF NOT EXISTS idx_sales_tenant_created
		 ON sales (tenant_id, created_at DESC)
		 WHERE deleted_at IS NULL`,

		// ListProducts, InventoryHealth y el catálogo filtran tenant_id +
		// is_available. El prefijo de igualdad ya ayuda (stock/min_stock son
		// rangos que el índice no cubre del todo, pero acotan el escaneo).
		`CREATE INDEX IF NOT EXISTS idx_products_tenant_avail
		 ON products (tenant_id, is_available)
		 WHERE deleted_at IS NULL`,

		// /tasks (poll 15s) y el KDS leen order_tickets por tenant_id + status.
		`CREATE INDEX IF NOT EXISTS idx_order_tickets_tenant_status
		 ON order_tickets (tenant_id, status)
		 WHERE deleted_at IS NULL`,

		// Sync pull (collectServerChanges) lee por (tenant_id, updated_at) y usa
		// Unscoped — DEBE enviar borrados — así que estos van SIN partial
		// deleted_at (si no, los soft-deleted no entrarían al índice y el pull
		// los perdería). Solo aceleran el WHERE updated_at > last_sync.
		`CREATE INDEX IF NOT EXISTS idx_products_tenant_updated
		 ON products (tenant_id, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sales_tenant_updated
		 ON sales (tenant_id, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_customers_tenant_updated
		 ON customers (tenant_id, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_credit_accounts_tenant_updated
		 ON credit_accounts (tenant_id, updated_at)`,
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// applyTableAccountIndex crea el índice único PARCIAL que garantiza UNA sola
// cuenta de mesa ABIERTA por (tenant_id, label) — Spec 083, council
// BUG-DUP-ACCOUNT-RACE. Sin él, dos primeros pedidos concurrentes a la misma
// mesa crean cuentas duplicadas. Los handlers reintentan al violarlo (el 2º
// acumula en la cuenta del ganador). Defensivo/idempotente (IF NOT EXISTS); si
// hubiera duplicados previos en prod, falla y se loguea sin tumbar el arranque.
func applyTableAccountIndex(db *gorm.DB) error {
	return db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS uniq_open_table_account
		 ON order_tickets (tenant_id, label)
		 WHERE status IN ('nuevo','preparando','listo') AND deleted_at IS NULL`,
	).Error
}

// ensureBusinessTypesWhitelist keeps the Postgres validate_business_types()
// function in sync with models.ValidBusinessTypes. The CHECK constraint
// tenants_business_types_valid (migration 020) calls this function, so adding
// a new business type (e.g. academias_instituciones for F042) requires the
// function to accept it. Render runs AutoMigrate only — never the .sql
// migrations — so we CREATE OR REPLACE the function here (idempotent; the
// constraint keeps pointing at the same function, no DROP needed).
func ensureBusinessTypesWhitelist(db *gorm.DB) error {
	return db.Exec(`
CREATE OR REPLACE FUNCTION validate_business_types(val TEXT) RETURNS BOOLEAN AS $$
BEGIN
    IF val IS NULL OR val = '' OR val = '[]' THEN
        RETURN TRUE;
    END IF;
    RETURN (
        SELECT bool_and(
            v IN (
                'tienda_barrio',
                'minimercado',
                'deposito_construccion',
                'restaurante',
                'comidas_rapidas',
                'bar',
                'manufactura',
                'reparacion_muebles',
                'emprendimiento_general',
                'academias_instituciones',
                'proveedor_agricola',
                'proveedor_mayorista',
                'peluqueria_barberia'
            )
        )
        FROM jsonb_array_elements_text(val::jsonb) AS v
    );
END;
$$ LANGUAGE plpgsql IMMUTABLE;`).Error
}

// ensureMenuPlanIndexes — Spec 066. Suelta los índices únicos de una sola
// dimensión que existían cuando el menú era por-tenant, para que el nuevo
// ámbito por-(tenant, sede) pueda tener una fila por sede. Los índices únicos
// compuestos ya los crea AutoMigrate desde los tags. Idempotente.
func ensureMenuPlanIndexes(db *gorm.DB) error {
	stmts := []string{
		`DROP INDEX IF EXISTS idx_weekly_menu_plans_tenant_id`,
		`DROP INDEX IF EXISTS idx_mpo_tenant_date`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return err
		}
	}
	return nil
}
