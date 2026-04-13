-- +goose Up
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS receipt_header TEXT DEFAULT '';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS receipt_footer TEXT DEFAULT '';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS printer_mac_address VARCHAR DEFAULT '';

-- +goose Down
ALTER TABLE tenants DROP COLUMN IF EXISTS receipt_header;
ALTER TABLE tenants DROP COLUMN IF EXISTS receipt_footer;
ALTER TABLE tenants DROP COLUMN IF EXISTS printer_mac_address;
