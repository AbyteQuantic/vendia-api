-- +goose Up
-- Migration 019: Marketing engine — combo promotions + AI banner + opt-in.
-- Layered on top of the existing single-product promotions table so legacy
-- rows keep working. Net effect:
--   * `promotions` gains `name`, `start_date`, `end_date`, `stock_limit`,
--     `banner_image_url`. `product_uuid` / `orig_price` / `promo_price` /
--     `product_name` stay on the table as NULLABLE so the handler can
--     operate either in legacy single-product mode or in combo mode.
--   * `promotion_items` lets one promotion bundle many products, each
--     with its own quantity — this is the combo unit that the Flutter
--     PromoBuilder writes and the public menu reads.
--   * `customers.marketing_opt_in` enables lawful WhatsApp broadcasts.
--     Default false for every row — existing customers must actively
--     opt in before they receive any marketing message.
--   * Indexes target the two hot queries: (a) active promos for a
--     tenant ordered by start_date for the carousel, (b) items of a
--     single promotion for the menu expansion.
--
-- Partial index on (tenant_id, is_active, start_date) covers the public
-- catalog listing and the app's "current promos" widget.

ALTER TABLE promotions
    ADD COLUMN IF NOT EXISTS name              TEXT,
    ADD COLUMN IF NOT EXISTS start_date        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS end_date          TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS stock_limit       INTEGER,
    ADD COLUMN IF NOT EXISTS banner_image_url  TEXT;

-- Make the legacy single-product fields nullable so combo promos don't
-- need to fake a value. Existing rows keep their values.
ALTER TABLE promotions ALTER COLUMN product_uuid  DROP NOT NULL;
ALTER TABLE promotions ALTER COLUMN product_name  DROP NOT NULL;
ALTER TABLE promotions ALTER COLUMN orig_price    DROP NOT NULL;
ALTER TABLE promotions ALTER COLUMN promo_price   DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_promotions_tenant_active
    ON promotions (tenant_id, is_active, start_date DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS promotion_items (
    id              UUID         PRIMARY KEY,
    promotion_id    UUID         NOT NULL REFERENCES promotions(id) ON DELETE CASCADE,
    product_id      UUID         NOT NULL REFERENCES products(id)   ON DELETE RESTRICT,
    quantity        INTEGER      NOT NULL DEFAULT 1 CHECK (quantity > 0),
    promo_price     NUMERIC(12,2) NOT NULL CHECK (promo_price >= 0),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_promotion_items_promotion
    ON promotion_items (promotion_id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_promotion_items_product
    ON promotion_items (product_id)
    WHERE deleted_at IS NULL;

ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS marketing_opt_in BOOLEAN NOT NULL DEFAULT FALSE;

-- Record this migration in goose's tracking table so future `goose up`
-- runs skip it cleanly. Idempotent — ON CONFLICT keeps the existing row.
INSERT INTO goose_db_version (version_id, is_applied)
SELECT 19, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 19);

-- +goose Down
DELETE FROM goose_db_version WHERE version_id = 19;

ALTER TABLE customers DROP COLUMN IF EXISTS marketing_opt_in;

DROP INDEX IF EXISTS idx_promotion_items_product;
DROP INDEX IF EXISTS idx_promotion_items_promotion;
DROP TABLE IF EXISTS promotion_items;

DROP INDEX IF EXISTS idx_promotions_tenant_active;

ALTER TABLE promotions
    DROP COLUMN IF EXISTS banner_image_url,
    DROP COLUMN IF EXISTS stock_limit,
    DROP COLUMN IF EXISTS end_date,
    DROP COLUMN IF EXISTS start_date,
    DROP COLUMN IF EXISTS name;
