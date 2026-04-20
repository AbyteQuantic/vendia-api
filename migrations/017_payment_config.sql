-- +goose Up
-- Migration 017: Express payment-config on the tenant. Nequi's custom-
-- scheme QR was rejected in production ("Este QR dejó de funcionar"),
-- so we pivot to a friction-less copy/paste flow on the debtor portal.
-- One primary method per tenant, stored on the tenant row so the public
-- fiado page doesn't need a join. The existing payment_methods table
-- stays as-is (legacy + multi-method power-users); it acts as a
-- fallback when these columns are empty.

ALTER TABLE tenants ADD COLUMN IF NOT EXISTS payment_method_name   VARCHAR(32)  NOT NULL DEFAULT '';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS payment_account_number VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS payment_account_holder VARCHAR(128) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tenants DROP COLUMN IF EXISTS payment_method_name;
ALTER TABLE tenants DROP COLUMN IF EXISTS payment_account_number;
ALTER TABLE tenants DROP COLUMN IF EXISTS payment_account_holder;
