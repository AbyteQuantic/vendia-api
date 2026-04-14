-- +goose Up
-- Add fiado handshake fields to credit_accounts
ALTER TABLE credit_accounts ADD COLUMN IF NOT EXISTS fiado_token UUID DEFAULT gen_random_uuid();
ALTER TABLE credit_accounts ADD COLUMN IF NOT EXISTS fiado_status TEXT DEFAULT 'none';
ALTER TABLE credit_accounts ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ;
ALTER TABLE credit_accounts ADD COLUMN IF NOT EXISTS accepted_ip TEXT DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_credit_accounts_fiado_token
    ON credit_accounts(fiado_token) WHERE fiado_token IS NOT NULL;

-- Add email to customers
ALTER TABLE customers ADD COLUMN IF NOT EXISTS email TEXT DEFAULT '';

-- +goose Down
ALTER TABLE credit_accounts DROP COLUMN IF EXISTS fiado_token;
ALTER TABLE credit_accounts DROP COLUMN IF EXISTS fiado_status;
ALTER TABLE credit_accounts DROP COLUMN IF EXISTS accepted_at;
ALTER TABLE credit_accounts DROP COLUMN IF EXISTS accepted_ip;
ALTER TABLE customers DROP COLUMN IF EXISTS email;
