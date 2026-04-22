-- +goose Up
-- Migration 022: Subscriptions engine — Reverse Trial (7 days) + SaaS gating.
--
-- Rationale:
--   Phase 1 of the SaaS monetisation roadmap. Every new tenant opens in
--   `TRIAL` for 7 days with full PRO access; once the window elapses,
--   the app degrades to `FREE` and premium modules return 403
--   `premium_expired` so the Flutter client can show a soft paywall.
--
--   The column `tenants.subscription_status` already exists (legacy)
--   but its values ('trial' / 'active' / 'suspended' / 'cancelled') do
--   not map cleanly to the new four-state machine the billing engine
--   needs. This migration introduces a dedicated `tenant_subscriptions`
--   row keyed 1:1 with the tenant so the lifecycle lives in one place,
--   queries stay O(1), and the old column can be retired in a later
--   migration without breaking the admin dashboard.
--
--   State machine (hard-enforced by the app):
--     TRIAL ──(trial_ends_at <= now)──▶ FREE
--     TRIAL ──(manual upgrade)────────▶ PRO_ACTIVE
--     FREE  ──(manual upgrade)────────▶ PRO_ACTIVE
--     PRO_ACTIVE ──(payment failure)──▶ PRO_PAST_DUE
--     PRO_PAST_DUE ──(retry success)──▶ PRO_ACTIVE
--     PRO_PAST_DUE ──(grace over)─────▶ FREE

CREATE TABLE IF NOT EXISTS tenant_subscriptions (
    tenant_id     UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    status        VARCHAR(32) NOT NULL
        CHECK (status IN ('TRIAL', 'FREE', 'PRO_ACTIVE', 'PRO_PAST_DUE'))
        DEFAULT 'TRIAL',
    trial_ends_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_status
    ON tenant_subscriptions(status);
CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_trial_ends
    ON tenant_subscriptions(trial_ends_at) WHERE status = 'TRIAL';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bootstrap_tenant_subscription()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO tenant_subscriptions (tenant_id, status, trial_ends_at)
    VALUES (NEW.id, 'TRIAL', NOW() + INTERVAL '7 days')
    ON CONFLICT (tenant_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_bootstrap_tenant_subscription ON tenants;
CREATE TRIGGER trg_bootstrap_tenant_subscription
    AFTER INSERT ON tenants
    FOR EACH ROW
    EXECUTE FUNCTION bootstrap_tenant_subscription();

-- Backfill existing tenants. Those with a legacy subscription_status of
-- 'active' map to PRO_ACTIVE; anyone else starts a fresh 7-day trial so
-- pre-migration tenants are never worse off than a brand-new signup.
INSERT INTO tenant_subscriptions (tenant_id, status, trial_ends_at, created_at)
SELECT
    t.id,
    CASE
        WHEN t.subscription_status = 'active' THEN 'PRO_ACTIVE'
        ELSE 'TRIAL'
    END,
    CASE
        WHEN t.subscription_status = 'active' THEN NULL
        ELSE NOW() + INTERVAL '7 days'
    END,
    COALESCE(t.created_at, NOW())
FROM tenants t
LEFT JOIN tenant_subscriptions ts ON ts.tenant_id = t.id
WHERE ts.tenant_id IS NULL;

INSERT INTO goose_db_version (version_id, is_applied)
SELECT 22, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 22);

-- +goose Down
DELETE FROM goose_db_version WHERE version_id = 22;
DROP TRIGGER IF EXISTS trg_bootstrap_tenant_subscription ON tenants;
DROP FUNCTION IF EXISTS bootstrap_tenant_subscription();
DROP TABLE IF EXISTS tenant_subscriptions;
