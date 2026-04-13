-- +goose Up
-- Add spatial grid columns and capacity to existing tables
ALTER TABLE tables ADD COLUMN IF NOT EXISTS grid_x INT DEFAULT 0;
ALTER TABLE tables ADD COLUMN IF NOT EXISTS grid_y INT DEFAULT 0;
ALTER TABLE tables ADD COLUMN IF NOT EXISTS capacity INT DEFAULT 4;

-- +goose Down
ALTER TABLE tables DROP COLUMN IF EXISTS grid_x;
ALTER TABLE tables DROP COLUMN IF EXISTS grid_y;
ALTER TABLE tables DROP COLUMN IF EXISTS capacity;
