// Spec: specs/014-inventario-solido-scope-sede/spec.md
package database

import (
	"fmt"
	"log"

	"gorm.io/gorm"
)

// branchBackfillTable describes one operational table the backfill
// repairs: its name and whether it carries a `deleted_at` column.
//
// Why the soft-delete flag: every table except inventory_movements
// embeds BaseModel and is soft-deletable, so its UPDATE must skip
// already-deleted rows (Spec 014 §6 — "el backfill solo toca filas no
// borradas"). inventory_movements is an append-only kardex with NO
// deleted_at column, so adding the clause would raise a SQL error.
type branchBackfillTable struct {
	name          string
	hasSoftDelete bool
}

// branchBackfillTables lists every operational table that carries a
// nullable branch_id column and must be repaired by the backfill.
//
// Why these five (Spec 014 §3 / FR-03):
//   - products            — the bug that triggered the feature: a NULL
//     branch_id hides the product from Inventario
//     and the Dashboard.
//   - sales               — a NULL-branch sale drops out of sede-scoped
//     reporting.
//   - inventory_movements — the kardex trail must stay scoped with its
//     product so per-sede inventory math is exact.
//     Append-only — no deleted_at column.
//   - credit_accounts     — fiado scoped to a sede.
//   - order_tickets       — KDS tickets scoped to a sede.
var branchBackfillTables = []branchBackfillTable{
	{name: "products", hasSoftDelete: true},
	{name: "sales", hasSoftDelete: true},
	{name: "inventory_movements", hasSoftDelete: false},
	{name: "credit_accounts", hasSoftDelete: true},
	{name: "order_tickets", hasSoftDelete: true},
}

// defaultBranchByTenant resolves the "sede por defecto" of every tenant:
// the oldest non-deleted branch (Spec 014 §6). For a mono-sede tenant
// that is simply its only sede; for a multi-sede tenant the deterministic
// `created_at` tie-breaker keeps the choice stable across re-runs.
//
// The result maps tenant_id → branch_id. A tenant with no live branch is
// absent from the map — the caller skips it (we never invent a sede).
//
// The query groups branches by tenant and reads MIN(created_at); a second
// pass matches each tenant's minimum back to a concrete branch id. This
// avoids the Postgres-only `DISTINCT ON`, so the same code path is
// exercised by the SQLite-backed unit tests (Art. VIII) and by the
// Postgres production boot.
func defaultBranchByTenant(db *gorm.DB) (map[string]string, error) {
	type branchRow struct {
		ID       string
		TenantID string
	}
	var rows []branchRow
	if err := db.Table("branches").
		Select("id, tenant_id, created_at").
		Where("deleted_at IS NULL").
		Order("tenant_id ASC, created_at ASC, id ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("scan branches for default-sede resolution: %w", err)
	}

	// rows are ordered (tenant_id, created_at, id) — the FIRST row seen
	// for each tenant is its oldest branch.
	defaults := make(map[string]string)
	for _, r := range rows {
		if _, seen := defaults[r.TenantID]; !seen {
			defaults[r.TenantID] = r.ID
		}
	}
	return defaults, nil
}

// BackfillBranchIDs assigns the tenant's default sede to every live
// (deleted_at IS NULL) operational row whose branch_id is still NULL,
// across products / sales / inventory_movements / credit_accounts /
// order_tickets (Spec 014 — FR-03).
//
// Why it exists: a product created by a mono-sede owner whose JWT carries
// no branch claim was inserted with branch_id NULL; sede-scoped reads
// (`WHERE branch_id = ?`) then excluded it, so the product vanished from
// Inventario and the Dashboard while still showing in "Vender". This
// function repairs the historical NULL rows so every screen sees the same
// catalog again (AC-01).
//
// Idempotency (Art. II): every UPDATE is gated by `branch_id IS NULL`, so
// a row that already carries a sede is never re-pointed and a second run
// on a fully-scoped database is a no-op. Tenants with no branch at all
// are skipped — there is nothing to assign and we never borrow another
// tenant's sede (Art. III multi-tenant isolation).
//
// Migrations (Art. X): Render deploys run GORM AutoMigrate only, never
// the goose `.sql` files, so this backfill lives in the Go bootstrap and
// is wired into cmd/server/main.go right after AutoMigrate. Every boot
// self-heals.
//
// Returns the total number of rows touched across all tables. Errors are
// wrapped with %w so the caller can log context; a wrapped error never
// aborts the boot (a stranded NULL row is preferable to a crashing
// deploy — Art. X spirit).
func BackfillBranchIDs(db *gorm.DB) (int, error) {
	defaults, err := defaultBranchByTenant(db)
	if err != nil {
		return 0, fmt.Errorf("resolve default branches: %w", err)
	}
	if len(defaults) == 0 {
		// No tenant has a branch — nothing to backfill.
		return 0, nil
	}

	total := 0
	for _, table := range branchBackfillTables {
		// Soft-deletable tables must skip already-deleted rows; the
		// append-only kardex (inventory_movements) has no deleted_at.
		whereClause := "tenant_id = ? AND branch_id IS NULL"
		if table.hasSoftDelete {
			whereClause += " AND deleted_at IS NULL"
		}
		for tenantID, branchID := range defaults {
			// Per-tenant, per-table UPDATE. Parameterised (Art. VI):
			// the table name is from a fixed allow-list, never user
			// input, so interpolating it is safe.
			res := db.Exec(
				"UPDATE "+table.name+" SET branch_id = ? WHERE "+whereClause,
				branchID, tenantID)
			if res.Error != nil {
				return total, fmt.Errorf("backfill branch_id on %s for tenant %s: %w",
					table.name, tenantID, res.Error)
			}
			total += int(res.RowsAffected)
		}
	}

	if total > 0 {
		log.Printf("[BOOTSTRAP] backfill branch_id: %d filas asignadas a su sede por defecto", total)
	}
	return total, nil
}
