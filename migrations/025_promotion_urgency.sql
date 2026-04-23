-- +goose Up
-- Migration 025: Promotion Urgency & FOMO
-- Standardize promotion expiration and stock limits.

ALTER TABLE promotions ADD COLUMN IF NOT EXISTS stock_limit INT;
ALTER TABLE promotions ADD COLUMN IF NOT EXISTS start_date TIMESTAMPTZ;
ALTER TABLE promotions ADD COLUMN IF NOT EXISTS end_date TIMESTAMPTZ;

-- Backfill end_date from expires_at if it looks like a valid date
UPDATE promotions 
SET end_date = expires_at::TIMESTAMPTZ 
WHERE expires_at IS NOT NULL 
  AND expires_at <> '' 
  AND end_date IS NULL;

-- +goose Down
ALTER TABLE promotions DROP COLUMN IF EXISTS end_date;
ALTER TABLE promotions DROP COLUMN IF EXISTS start_date;
ALTER TABLE promotions DROP COLUMN IF EXISTS stock_limit;
