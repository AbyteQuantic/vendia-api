-- +goose Up
-- Migration 020: Agnostic billing engine + unified business type taxonomy.
--
-- Rationale — see P0 audit of 2026-04-20:
--   * `business_types` was cosmetic metadata; no handler read it. We now
--     (a) remap legacy values to the unified taxonomy, and (b) validate
--     every array element against a whitelist via a CHECK constraint so
--     typos can't reach production.
--   * `sale_items.product_id` was NOT NULL → sales were physically
--     coupled to the inventory table. A salon or repair shop could not
--     record a haircut or a furniture repair because there was no line
--     item without a product FK. We flexibilise the column and add
--     `is_service` + `custom_description` + `custom_unit_price` so the
--     same Sale row can mix retail SKUs and ad-hoc service charges.
--   * `sales` gains `tax_amount`, `tip_amount`, and a frozen snapshot
--     of the customer (name + phone). Reprinting a two-year-old receipt
--     must not depend on the customer FK still existing or still
--     matching the name they had at the time of sale.
--
-- NOTE: `tenants.business_types` is stored as TEXT (GORM
-- `serializer:json` produced a TEXT column despite migration 005's
-- JSONB intent — AutoMigrate won the race). We validate the TEXT
-- content by casting to JSONB inside the validator function.
--
-- This migration is additive: every existing sale row stays valid
-- because defaults are 0/'', and the check on business_types is
-- enforced only against the WHITELISTED values; legacy 'muebles',
-- 'miscelanea', 'reparacion' rows are remapped before the CHECK
-- is installed.

-- ── 1. Unified business-type taxonomy ───────────────────────────────────────

-- Remap legacy values in-place.
UPDATE tenants
   SET business_types = (
       SELECT jsonb_agg(
           CASE
               WHEN elem = 'muebles'     THEN 'reparacion_muebles'
               WHEN elem = 'miscelanea'  THEN 'emprendimiento_general'
               WHEN elem = 'reparacion'  THEN 'reparacion_muebles'
               ELSE elem
           END
       )::text
       FROM jsonb_array_elements_text(business_types::jsonb) AS elem
   )
 WHERE business_types ~ '(muebles|miscelanea|"reparacion")';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION validate_business_types(val TEXT) RETURNS BOOLEAN AS $$
BEGIN
    IF val IS NULL OR val = '' OR val = '[]' THEN
        RETURN TRUE;
    END IF;
    RETURN (
        SELECT bool_and(
            v IN (
                'tienda_barrio',
                'minimercado',
                'deposito_construccion',
                'restaurante',
                'comidas_rapidas',
                'bar',
                'manufactura',
                'reparacion_muebles',
                'emprendimiento_general'
            )
        )
        FROM jsonb_array_elements_text(val::jsonb) AS v
    );
END;
$$ LANGUAGE plpgsql IMMUTABLE;
-- +goose StatementEnd

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_business_types_valid;
ALTER TABLE tenants
    ADD CONSTRAINT tenants_business_types_valid
        CHECK (validate_business_types(business_types));

-- ── 2. Agnostic sale_items (product_id nullable + service columns) ─────────

ALTER TABLE sale_items ALTER COLUMN product_id DROP NOT NULL;

ALTER TABLE sale_items
    ADD COLUMN IF NOT EXISTS is_service          BOOLEAN       NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS custom_description  VARCHAR(256)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS custom_unit_price   NUMERIC(12,2) NOT NULL DEFAULT 0;

ALTER TABLE sale_items DROP CONSTRAINT IF EXISTS sale_items_product_or_service;
ALTER TABLE sale_items
    ADD CONSTRAINT sale_items_product_or_service CHECK (
        (is_service = FALSE AND product_id IS NOT NULL) OR
        (is_service = TRUE  AND product_id IS NULL AND char_length(custom_description) > 0)
    );

-- ── 3. Sales: tax, tip, frozen customer snapshot ───────────────────────────

ALTER TABLE sales
    ADD COLUMN IF NOT EXISTS tax_amount              NUMERIC(12,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tip_amount              NUMERIC(12,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS customer_name_snapshot  VARCHAR(128)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS customer_phone_snapshot VARCHAR(32)   NOT NULL DEFAULT '';

-- ── 4. Goose tracking ──────────────────────────────────────────────────────

INSERT INTO goose_db_version (version_id, is_applied)
SELECT 20, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 20);

-- +goose Down
DELETE FROM goose_db_version WHERE version_id = 20;

ALTER TABLE sales DROP COLUMN IF EXISTS customer_phone_snapshot;
ALTER TABLE sales DROP COLUMN IF EXISTS customer_name_snapshot;
ALTER TABLE sales DROP COLUMN IF EXISTS tip_amount;
ALTER TABLE sales DROP COLUMN IF EXISTS tax_amount;

ALTER TABLE sale_items DROP CONSTRAINT IF EXISTS sale_items_product_or_service;
ALTER TABLE sale_items DROP COLUMN IF EXISTS custom_unit_price;
ALTER TABLE sale_items DROP COLUMN IF EXISTS custom_description;
ALTER TABLE sale_items DROP COLUMN IF EXISTS is_service;

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_business_types_valid;
DROP FUNCTION IF EXISTS validate_business_types(TEXT);
