-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS catalog_products (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    brand      TEXT NOT NULL DEFAULT '',
    image_url  TEXT NOT NULL DEFAULT '',
    barcode    TEXT NOT NULL DEFAULT '',
    category   TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_catalog_products_name_brand
    ON catalog_products (LOWER(name), LOWER(brand));

CREATE INDEX IF NOT EXISTS idx_catalog_products_name_trgm
    ON catalog_products USING gin (name gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_catalog_products_fetched_at
    ON catalog_products (fetched_at);

-- +goose Down
DROP TABLE IF EXISTS catalog_products;
