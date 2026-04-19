-- +goose Up
-- Migration 014: Trazabilidad multi-usuario/multi-sucursal + PIN del propietario
-- Aditiva: todas las columnas son nullable para no romper filas existentes.

-- 1. Sales: quien vendió y en qué sucursal
ALTER TABLE sales       ADD COLUMN IF NOT EXISTS created_by UUID;
ALTER TABLE sales       ADD COLUMN IF NOT EXISTS branch_id  UUID;
CREATE INDEX IF NOT EXISTS idx_sales_created_by ON sales(created_by);
CREATE INDEX IF NOT EXISTS idx_sales_branch_id  ON sales(branch_id);

-- 2. Order tickets (KDS / mesas)
ALTER TABLE order_tickets ADD COLUMN IF NOT EXISTS created_by UUID;
ALTER TABLE order_tickets ADD COLUMN IF NOT EXISTS branch_id  UUID;
CREATE INDEX IF NOT EXISTS idx_order_tickets_created_by ON order_tickets(created_by);
CREATE INDEX IF NOT EXISTS idx_order_tickets_branch_id  ON order_tickets(branch_id);

-- 3. Credit accounts (El Fiar)
ALTER TABLE credit_accounts ADD COLUMN IF NOT EXISTS created_by UUID;
ALTER TABLE credit_accounts ADD COLUMN IF NOT EXISTS branch_id  UUID;
CREATE INDEX IF NOT EXISTS idx_credit_accounts_created_by ON credit_accounts(created_by);
CREATE INDEX IF NOT EXISTS idx_credit_accounts_branch_id  ON credit_accounts(branch_id);

-- 4. Credit payments (abonos)
ALTER TABLE credit_payments ADD COLUMN IF NOT EXISTS created_by UUID;
ALTER TABLE credit_payments ADD COLUMN IF NOT EXISTS branch_id  UUID;
CREATE INDEX IF NOT EXISTS idx_credit_payments_created_by ON credit_payments(created_by);
CREATE INDEX IF NOT EXISTS idx_credit_payments_branch_id  ON credit_payments(branch_id);

-- 5. Products (inventario)
ALTER TABLE products ADD COLUMN IF NOT EXISTS created_by UUID;
ALTER TABLE products ADD COLUMN IF NOT EXISTS branch_id  UUID;
CREATE INDEX IF NOT EXISTS idx_products_created_by ON products(created_by);
CREATE INDEX IF NOT EXISTS idx_products_branch_id  ON products(branch_id);

-- 6. Tenants: hash del PIN del owner (handshake para fiado del cajero, fase 3)
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS owner_pin_hash TEXT;

-- +goose Down
ALTER TABLE sales           DROP COLUMN IF EXISTS created_by;
ALTER TABLE sales           DROP COLUMN IF EXISTS branch_id;
ALTER TABLE order_tickets   DROP COLUMN IF EXISTS created_by;
ALTER TABLE order_tickets   DROP COLUMN IF EXISTS branch_id;
ALTER TABLE credit_accounts DROP COLUMN IF EXISTS created_by;
ALTER TABLE credit_accounts DROP COLUMN IF EXISTS branch_id;
ALTER TABLE credit_payments DROP COLUMN IF EXISTS created_by;
ALTER TABLE credit_payments DROP COLUMN IF EXISTS branch_id;
ALTER TABLE products        DROP COLUMN IF EXISTS created_by;
ALTER TABLE products        DROP COLUMN IF EXISTS branch_id;
ALTER TABLE tenants         DROP COLUMN IF EXISTS owner_pin_hash;
