-- +goose Up
-- Add fiados and margin settings to tenants
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS enable_fiados BOOLEAN DEFAULT true;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS default_margin NUMERIC DEFAULT 20.0;

-- Create payment_methods table
CREATE TABLE IF NOT EXISTS payment_methods (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    tenant_id UUID NOT NULL,
    name TEXT NOT NULL,
    account_details TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN DEFAULT true
);
CREATE INDEX IF NOT EXISTS idx_payment_methods_tenant_id ON payment_methods(tenant_id);
CREATE INDEX IF NOT EXISTS idx_payment_methods_deleted_at ON payment_methods(deleted_at);

-- +goose Down
ALTER TABLE tenants DROP COLUMN IF EXISTS enable_fiados;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_margin;
DROP TABLE IF EXISTS payment_methods;
