-- +goose Up
-- Migration 025: hang every employee off a specific branch.
--
-- Rationale:
--   The multi-branch refactor (Phase 5) requires employees to be
--   scoped to a single sede so the POS / KDS / inventory reads can
--   filter by branch_id. Migration 014 already wired branch_id onto
--   sales, order_tickets, credit_accounts, credit_payments and
--   products — employees slipped through the net because the model
--   was still single-sede at the time.
--
--   Steps:
--     1. Add nullable branch_id column (so the ALTER doesn't lock
--        out every tenant without a branch row).
--     2. Ensure every tenant has at least one branch. Any tenant
--        without one gets a synthetic "Sede Principal" created here
--        — the TenantRegister handler has always seeded a branch,
--        so this backfill only catches tenants that predate
--        migration 009 or whose branch row was manually deleted.
--     3. Assign every employee's branch_id to the oldest branch of
--        its tenant (stable, deterministic).
--     4. Index the column.
--
--   We leave the column nullable for now even after backfill — the
--   NOT NULL constraint lives at the application layer (handlers
--   reject missing branch_id on employee create). Doing it at the
--   DB level would require a full table rewrite for large tenants
--   and only catches a narrow class of bugs the application can
--   handle with a cheaper 400 response.

ALTER TABLE employees ADD COLUMN IF NOT EXISTS branch_id UUID;

-- Create a "Sede Principal" for any tenant that somehow lost or
-- never had one. The ::uuid cast is because BaseModel uses gen_random_uuid.
INSERT INTO branches (id, tenant_id, name, address, is_active,
                      created_at, updated_at)
SELECT gen_random_uuid(), t.id, 'Sede Principal',
       COALESCE(t.address, ''), TRUE, NOW(), NOW()
FROM tenants t
WHERE NOT EXISTS (
    SELECT 1 FROM branches b
    WHERE b.tenant_id = t.id AND b.deleted_at IS NULL
);

-- Backfill employee.branch_id with the oldest branch of the tenant.
UPDATE employees e
   SET branch_id = sub.id
FROM (
    SELECT DISTINCT ON (tenant_id) tenant_id, id
    FROM branches
    WHERE deleted_at IS NULL
    ORDER BY tenant_id, created_at ASC
) sub
WHERE e.tenant_id = sub.tenant_id
  AND e.branch_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_employees_branch_id
    ON employees(branch_id);

INSERT INTO goose_db_version (version_id, is_applied)
SELECT 25, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 25);

-- +goose Down
DELETE FROM goose_db_version WHERE version_id = 25;
DROP INDEX IF EXISTS idx_employees_branch_id;
ALTER TABLE employees DROP COLUMN IF EXISTS branch_id;
