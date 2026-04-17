-- +goose Up

CREATE TABLE IF NOT EXISTS notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id UUID NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL DEFAULT 'info',
    is_read BOOLEAN DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_notifications_tenant_id ON notifications(tenant_id);
CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications(tenant_id, is_read) WHERE is_read = false;

CREATE TABLE IF NOT EXISTS online_orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id UUID NOT NULL,
    customer_name TEXT NOT NULL,
    customer_phone TEXT NOT NULL DEFAULT '',
    delivery_type TEXT NOT NULL DEFAULT 'pickup',
    status TEXT NOT NULL DEFAULT 'pending',
    total_amount NUMERIC NOT NULL DEFAULT 0,
    items JSONB NOT NULL DEFAULT '[]',
    notes TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_online_orders_tenant_id ON online_orders(tenant_id);
CREATE INDEX IF NOT EXISTS idx_online_orders_status ON online_orders(tenant_id, status);

-- +goose Down
DROP TABLE IF EXISTS online_orders;
DROP TABLE IF EXISTS notifications;
