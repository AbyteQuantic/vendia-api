-- +goose Up
-- Migration 018: Perishable goods support — expiration date on products.
-- Required by the end-to-end expiry flow (manual creation, invoice OCR,
-- FEFO rotation, near-expiry alerts).
--
-- The Go model declared `ExpiryDate *string` so GORM AutoMigrate silently
-- created the column as TEXT in environments where the server had started.
-- We now version it explicitly and switch the storage type to DATE for
-- correct day-resolution semantics (shelf-life decisions are never
-- time-of-day sensitive) and better query ergonomics. Empty strings
-- from the legacy TEXT column — if any slipped in — are normalised to
-- NULL before the ALTER TYPE.
--
-- Index: partial, only indexes rows that actually carry an expiry date,
-- keeping the structure small and fast for the "expiring in N days"
-- queries used by the alerts endpoint and the AI promotion engine.

-- Create the column if it doesn't exist yet (fresh install path).
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS expiry_date DATE;

-- If the column already exists as TEXT (AutoMigrate legacy), normalise
-- empty strings and convert to DATE. USING clause is a no-op when the
-- column is already DATE.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'products'
          AND column_name = 'expiry_date'
          AND data_type = 'text'
    ) THEN
        UPDATE products SET expiry_date = NULL
         WHERE expiry_date IS NOT NULL AND btrim(expiry_date) = '';
        ALTER TABLE products
            ALTER COLUMN expiry_date TYPE DATE
            USING NULLIF(btrim(expiry_date), '')::DATE;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_products_expiry_date
    ON products (tenant_id, expiry_date)
    WHERE expiry_date IS NOT NULL AND deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_products_expiry_date;
ALTER TABLE products DROP COLUMN IF EXISTS expiry_date;
