-- +goose Up
-- Migration 026: backfill branch_id on operational tables so the
-- Phase-6 branch-isolation filters return sensible data on day one.
--
-- Migrations 014 + 009 added branch_id UUID columns to sales,
-- credit_accounts, credit_payments, products and order_tickets —
-- all nullable, because the Flutter client at the time didn't know
-- which sede it belonged to. With Phase-6 the read/write handlers
-- now filter by branch_id, which means every legacy NULL row is
-- invisible to a sede-scoped query. This migration assigns those
-- orphans to the tenant's oldest active branch — the same default
-- sede `TenantRegister` creates on signup.
--
-- Idempotent: `WHERE branch_id IS NULL` skips rows that already
-- have a valid assignment. Safe to re-run.

-- 1. Backfill products → oldest branch of their tenant.
UPDATE products p
   SET branch_id = sub.id
FROM (
    SELECT DISTINCT ON (tenant_id) tenant_id, id
    FROM branches
    WHERE deleted_at IS NULL
    ORDER BY tenant_id, created_at ASC
) sub
WHERE p.tenant_id = sub.tenant_id
  AND p.branch_id IS NULL;

-- 2. Backfill sales.
UPDATE sales s
   SET branch_id = sub.id
FROM (
    SELECT DISTINCT ON (tenant_id) tenant_id, id
    FROM branches
    WHERE deleted_at IS NULL
    ORDER BY tenant_id, created_at ASC
) sub
WHERE s.tenant_id = sub.tenant_id
  AND s.branch_id IS NULL;

-- 3. Backfill credit_accounts.
UPDATE credit_accounts c
   SET branch_id = sub.id
FROM (
    SELECT DISTINCT ON (tenant_id) tenant_id, id
    FROM branches
    WHERE deleted_at IS NULL
    ORDER BY tenant_id, created_at ASC
) sub
WHERE c.tenant_id = sub.tenant_id
  AND c.branch_id IS NULL;

-- 4. Backfill credit_payments. credit_payments.tenant_id is
--    denormalised at write time so the JOIN is a simple tenant match.
UPDATE credit_payments cp
   SET branch_id = sub.id
FROM (
    SELECT DISTINCT ON (tenant_id) tenant_id, id
    FROM branches
    WHERE deleted_at IS NULL
    ORDER BY tenant_id, created_at ASC
) sub
WHERE cp.tenant_id = sub.tenant_id
  AND cp.branch_id IS NULL;

-- 5. Backfill order_tickets (KDS).
UPDATE order_tickets o
   SET branch_id = sub.id
FROM (
    SELECT DISTINCT ON (tenant_id) tenant_id, id
    FROM branches
    WHERE deleted_at IS NULL
    ORDER BY tenant_id, created_at ASC
) sub
WHERE o.tenant_id = sub.tenant_id
  AND o.branch_id IS NULL;

INSERT INTO goose_db_version (version_id, is_applied)
SELECT 26, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 26);

-- +goose Down
-- Revert is a no-op: NULLing branch_id on data rows would lose the
-- mapping and re-hide the rows from Phase-6 filters. If you need to
-- unwind Phase-6, run migration 025 Down first (which drops the
-- column entirely) rather than re-NULLing these.
DELETE FROM goose_db_version WHERE version_id = 26;
