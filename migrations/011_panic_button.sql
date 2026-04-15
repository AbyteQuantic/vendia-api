-- +goose Up
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS panic_message TEXT DEFAULT '';

CREATE TABLE IF NOT EXISTS emergency_contacts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    tenant_id UUID NOT NULL,
    name TEXT NOT NULL,
    phone_number TEXT NOT NULL,
    contact_method TEXT NOT NULL DEFAULT 'whatsapp'
);
CREATE INDEX IF NOT EXISTS idx_emergency_contacts_tenant_id ON emergency_contacts(tenant_id);

-- +goose Down
ALTER TABLE tenants DROP COLUMN IF EXISTS panic_message;
DROP TABLE IF EXISTS emergency_contacts;
