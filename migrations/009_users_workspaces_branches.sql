-- +goose Up

-- 1. Users table (global identity, separated from tenant)
CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ,
    phone         TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone ON users(phone) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users(deleted_at);

-- 2. Branches table (sucursales per tenant)
CREATE TABLE IF NOT EXISTS branches (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    tenant_id  UUID NOT NULL,
    name       TEXT NOT NULL,
    address    TEXT NOT NULL DEFAULT '',
    is_active  BOOLEAN DEFAULT true
);
CREATE INDEX IF NOT EXISTS idx_branches_tenant_id ON branches(tenant_id);
CREATE INDEX IF NOT EXISTS idx_branches_deleted_at ON branches(deleted_at);

-- 3. User workspaces pivot table
CREATE TABLE IF NOT EXISTS user_workspaces (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    user_id    UUID NOT NULL,
    tenant_id  UUID NOT NULL,
    branch_id  UUID,
    role       TEXT NOT NULL DEFAULT 'owner',
    is_default BOOLEAN DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_user_workspaces_user_id ON user_workspaces(user_id);
CREATE INDEX IF NOT EXISTS idx_user_workspaces_tenant_id ON user_workspaces(tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_workspaces_unique
    ON user_workspaces(user_id, tenant_id, COALESCE(branch_id, '00000000-0000-0000-0000-000000000000'))
    WHERE deleted_at IS NULL;

-- 4. Add user_id to refresh_tokens for new auth flow
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS user_id UUID;

-- 5. Backfill: create a User for each existing Tenant owner
INSERT INTO users (id, created_at, updated_at, phone, name, password_hash)
SELECT gen_random_uuid(), t.created_at, t.updated_at, t.phone, t.owner_name, t.password_hash
FROM tenants t
WHERE t.phone IS NOT NULL AND t.phone != ''
ON CONFLICT DO NOTHING;

-- 6. Backfill: create default branch per tenant
INSERT INTO branches (tenant_id, name, address)
SELECT t.id, 'Principal', COALESCE(t.address, '')
FROM tenants t;

-- 7. Backfill: create workspace entry (owner role) for each user-tenant pair
INSERT INTO user_workspaces (user_id, tenant_id, branch_id, role, is_default)
SELECT u.id, t.id, b.id, 'owner', true
FROM tenants t
JOIN users u ON u.phone = t.phone
JOIN branches b ON b.tenant_id = t.id
WHERE b.name = 'Principal';

-- 8. Backfill: link refresh_tokens to users
UPDATE refresh_tokens rt
SET user_id = u.id
FROM tenants t
JOIN users u ON u.phone = t.phone
WHERE rt.tenant_id = t.id AND rt.user_id IS NULL;

-- +goose Down
DROP TABLE IF EXISTS user_workspaces;
DROP TABLE IF EXISTS branches;
DROP TABLE IF EXISTS users;
ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS user_id;
