-- +goose Up
-- Migration 015: link sales to their credit account so the public fiado
-- statement can show itemized detail of each purchase (product, quantity,
-- unit price). Nullable because cash/transfer/card sales are not linked.

ALTER TABLE sales ADD COLUMN IF NOT EXISTS credit_account_id UUID;
CREATE INDEX IF NOT EXISTS idx_sales_credit_account_id ON sales(credit_account_id);

-- +goose Down
ALTER TABLE sales DROP COLUMN IF EXISTS credit_account_id;
