-- +goose Up
-- Migration 023: Support tickets — Phase 3 SaaS support hub.
--
-- Rationale:
--   Tenants need a low-friction way to reach the ops team without
--   reaching for a separate app. This table is the minimum viable
--   ticketing primitive: a subject, a body, a two-state lifecycle
--   (OPEN → RESOLVED), and the (tenant, user) pair that raised it.
--
--   Richer features (threading, attachments, severity, SLAs) belong
--   in a later migration once real usage data tells us what's worth
--   paying for in a full ticketing SaaS vs. building in-house.

CREATE TABLE IF NOT EXISTS support_tickets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    subject     VARCHAR(160) NOT NULL,
    message     TEXT NOT NULL,
    status      VARCHAR(16) NOT NULL
        CHECK (status IN ('OPEN', 'RESOLVED'))
        DEFAULT 'OPEN',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The admin panel queries "open first, newest within status" so a
-- composite index on (status, created_at DESC) serves both the list
-- and the future badge count without extra passes.
CREATE INDEX IF NOT EXISTS idx_support_tickets_status_created
    ON support_tickets(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_support_tickets_tenant
    ON support_tickets(tenant_id);

INSERT INTO goose_db_version (version_id, is_applied)
SELECT 23, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 23);

-- +goose Down
DELETE FROM goose_db_version WHERE version_id = 23;
DROP TABLE IF EXISTS support_tickets;
