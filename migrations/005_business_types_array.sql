-- +goose Up
-- Migrate business_type TEXT → business_types JSONB (array of strings)
-- Consistent with sale_types column which also uses JSONB serialization.

ALTER TABLE tenants ADD COLUMN IF NOT EXISTS business_types JSONB DEFAULT '[]';

-- Migrate existing data: convert single text value into a JSON array
UPDATE tenants
SET business_types = jsonb_build_array(business_type)
WHERE business_type IS NOT NULL AND business_type != '';

-- Drop the old column
ALTER TABLE tenants DROP COLUMN IF EXISTS business_type;

-- +goose Down
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS business_type TEXT NOT NULL DEFAULT '';
UPDATE tenants SET business_type = business_types->>0 WHERE jsonb_array_length(business_types) > 0;
ALTER TABLE tenants DROP COLUMN IF EXISTS business_types;
