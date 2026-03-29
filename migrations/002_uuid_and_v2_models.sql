-- +goose Up
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Migrate existing tables from BIGSERIAL to UUID PKs.
-- For fresh databases this runs on empty tables. For existing DBs with data,
-- this performs the column swap with temporary columns.

-- ── tenants ──────────────────────────────────────────────────────────────────
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS new_id UUID DEFAULT gen_random_uuid();
UPDATE tenants SET new_id = gen_random_uuid() WHERE new_id IS NULL;

-- Drop FK refs to tenants(id) from children
ALTER TABLE employees DROP CONSTRAINT IF EXISTS fk_employees_tenant;
ALTER TABLE products DROP CONSTRAINT IF EXISTS fk_products_tenant;
ALTER TABLE sales DROP CONSTRAINT IF EXISTS fk_sales_tenant;
ALTER TABLE refresh_tokens DROP CONSTRAINT IF EXISTS fk_refresh_tokens_tenant;

-- Add UUID columns to children for the new FK
ALTER TABLE employees ADD COLUMN IF NOT EXISTS new_tenant_id UUID;
ALTER TABLE products ADD COLUMN IF NOT EXISTS new_tenant_id UUID;
ALTER TABLE sales ADD COLUMN IF NOT EXISTS new_tenant_id UUID;
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS new_tenant_id UUID;

UPDATE employees e SET new_tenant_id = t.new_id FROM tenants t WHERE t.id = e.tenant_id;
UPDATE products p SET new_tenant_id = t.new_id FROM tenants t WHERE t.id = p.tenant_id;
UPDATE sales s SET new_tenant_id = t.new_id FROM tenants t WHERE t.id = s.tenant_id;
UPDATE refresh_tokens rt SET new_tenant_id = t.new_id FROM tenants t WHERE t.id = rt.tenant_id;

-- ── sale_items ───────────────────────────────────────────────────────────────
ALTER TABLE sale_items ADD COLUMN IF NOT EXISTS new_id UUID DEFAULT gen_random_uuid();
ALTER TABLE sale_items ADD COLUMN IF NOT EXISTS new_sale_id UUID;
ALTER TABLE sale_items ADD COLUMN IF NOT EXISTS new_product_id UUID;

UPDATE sale_items si SET new_id = gen_random_uuid() WHERE si.new_id IS NULL;
UPDATE sale_items si SET new_sale_id = s.new_id FROM (SELECT id, new_id FROM sales WHERE new_id IS NOT NULL) s WHERE s.id = si.sale_id;
UPDATE sale_items si SET new_product_id = p.new_id FROM (SELECT id, new_id FROM products WHERE new_id IS NOT NULL) p WHERE p.id = si.product_id;

-- ── employees ────────────────────────────────────────────────────────────────
ALTER TABLE employees ADD COLUMN IF NOT EXISTS new_id UUID DEFAULT gen_random_uuid();
UPDATE employees SET new_id = gen_random_uuid() WHERE new_id IS NULL;

-- ── products ─────────────────────────────────────────────────────────────────
ALTER TABLE products ADD COLUMN IF NOT EXISTS new_id UUID DEFAULT gen_random_uuid();
UPDATE products SET new_id = gen_random_uuid() WHERE new_id IS NULL;

-- ── sales ────────────────────────────────────────────────────────────────────
ALTER TABLE sales ADD COLUMN IF NOT EXISTS new_id UUID DEFAULT gen_random_uuid();
UPDATE sales SET new_id = gen_random_uuid() WHERE new_id IS NULL;

-- ── refresh_tokens ───────────────────────────────────────────────────────────
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS new_id UUID DEFAULT gen_random_uuid();
UPDATE refresh_tokens SET new_id = gen_random_uuid() WHERE new_id IS NULL;

-- ── New v2 columns on existing tables ────────────────────────────────────────
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS charge_mode TEXT DEFAULT 'pre_payment';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS nequi_phone VARCHAR(15);
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS daviplata_phone VARCHAR(15);
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS last_sync_at TIMESTAMPTZ;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS pending_sync_ops INTEGER DEFAULT 0;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS subscription_status TEXT DEFAULT 'trial';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS subscription_ends_at TIMESTAMPTZ;

ALTER TABLE products ADD COLUMN IF NOT EXISTS requires_container BOOLEAN DEFAULT FALSE;
ALTER TABLE products ADD COLUMN IF NOT EXISTS container_price BIGINT DEFAULT 0;

ALTER TABLE sales ADD COLUMN IF NOT EXISTS customer_id UUID;
ALTER TABLE sales ADD COLUMN IF NOT EXISTS is_credit BOOLEAN DEFAULT FALSE;

ALTER TABLE sale_items ADD COLUMN IF NOT EXISTS is_container_charge BOOLEAN DEFAULT FALSE;

-- ── New v2 tables ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS customers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    tenant_id UUID NOT NULL,
    name TEXT NOT NULL,
    phone TEXT,
    notes TEXT
);
CREATE INDEX IF NOT EXISTS idx_customers_tenant_id ON customers(tenant_id);
CREATE INDEX IF NOT EXISTS idx_customers_deleted_at ON customers(deleted_at);

CREATE TABLE IF NOT EXISTS credit_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    tenant_id UUID NOT NULL,
    customer_id UUID NOT NULL,
    sale_id UUID NOT NULL,
    total_amount BIGINT NOT NULL,
    paid_amount BIGINT DEFAULT 0,
    status TEXT DEFAULT 'open',
    due_date TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_credit_accounts_tenant_id ON credit_accounts(tenant_id);
CREATE INDEX IF NOT EXISTS idx_credit_accounts_deleted_at ON credit_accounts(deleted_at);

CREATE TABLE IF NOT EXISTS credit_payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    credit_account_id UUID NOT NULL,
    amount BIGINT NOT NULL,
    payment_method TEXT DEFAULT 'cash',
    note TEXT
);
CREATE INDEX IF NOT EXISTS idx_credit_payments_credit_account_id ON credit_payments(credit_account_id);
CREATE INDEX IF NOT EXISTS idx_credit_payments_deleted_at ON credit_payments(deleted_at);

CREATE TABLE IF NOT EXISTS tables (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    tenant_id UUID NOT NULL,
    label TEXT NOT NULL,
    is_active BOOLEAN DEFAULT TRUE
);
CREATE INDEX IF NOT EXISTS idx_tables_tenant_id ON tables(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tables_deleted_at ON tables(deleted_at);

CREATE TABLE IF NOT EXISTS open_tabs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    tenant_id UUID NOT NULL,
    table_id UUID NOT NULL,
    status TEXT DEFAULT 'open',
    items JSONB,
    opened_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    sale_id UUID
);
CREATE INDEX IF NOT EXISTS idx_open_tabs_tenant_id ON open_tabs(tenant_id);
CREATE INDEX IF NOT EXISTS idx_open_tabs_deleted_at ON open_tabs(deleted_at);

CREATE TABLE IF NOT EXISTS admin_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    email TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    name TEXT NOT NULL,
    is_super_admin BOOLEAN DEFAULT TRUE
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_users_email ON admin_users(email);
CREATE INDEX IF NOT EXISTS idx_admin_users_deleted_at ON admin_users(deleted_at);

-- +goose Down

DROP TABLE IF EXISTS admin_users;
DROP TABLE IF EXISTS open_tabs;
DROP TABLE IF EXISTS tables;
DROP TABLE IF EXISTS credit_payments;
DROP TABLE IF EXISTS credit_accounts;
DROP TABLE IF EXISTS customers;

ALTER TABLE sale_items DROP COLUMN IF EXISTS is_container_charge;
ALTER TABLE sales DROP COLUMN IF EXISTS is_credit;
ALTER TABLE sales DROP COLUMN IF EXISTS customer_id;
ALTER TABLE products DROP COLUMN IF EXISTS container_price;
ALTER TABLE products DROP COLUMN IF EXISTS requires_container;
ALTER TABLE tenants DROP COLUMN IF EXISTS subscription_ends_at;
ALTER TABLE tenants DROP COLUMN IF EXISTS subscription_status;
ALTER TABLE tenants DROP COLUMN IF EXISTS pending_sync_ops;
ALTER TABLE tenants DROP COLUMN IF EXISTS last_sync_at;
ALTER TABLE tenants DROP COLUMN IF EXISTS daviplata_phone;
ALTER TABLE tenants DROP COLUMN IF EXISTS nequi_phone;
ALTER TABLE tenants DROP COLUMN IF EXISTS charge_mode;
