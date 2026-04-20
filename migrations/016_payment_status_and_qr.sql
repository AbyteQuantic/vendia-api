-- +goose Up
-- Migration 016: Zero-fee dynamic QR payments ("Plan B" — Nequi/Daviplata
-- transfer with amount pre-filled via deep link / EMVCo-like payload).
-- payment_status defaults to COMPLETED so pre-existing rows (all cash/
-- transfer sales until now) keep the analytics semantics they had.
-- New transfer/card flows can set it to PENDING while the QR is active
-- and flip to COMPLETED once the tendero visually confirms the Nequi SMS.

ALTER TABLE sales ADD COLUMN IF NOT EXISTS payment_status VARCHAR(32) NOT NULL DEFAULT 'COMPLETED';
ALTER TABLE sales ADD COLUMN IF NOT EXISTS dynamic_qr_payload TEXT;
CREATE INDEX IF NOT EXISTS idx_sales_payment_status ON sales(payment_status);

-- +goose Down
ALTER TABLE sales DROP COLUMN IF EXISTS payment_status;
ALTER TABLE sales DROP COLUMN IF EXISTS dynamic_qr_payload;
