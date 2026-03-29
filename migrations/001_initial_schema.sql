-- +goose Up

CREATE TABLE IF NOT EXISTS tenants (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,

    owner_name    TEXT NOT NULL,
    phone         TEXT NOT NULL,
    password_hash TEXT NOT NULL,

    business_name TEXT NOT NULL,
    business_type TEXT NOT NULL,
    razon_social  TEXT NOT NULL DEFAULT '',
    nit           TEXT NOT NULL DEFAULT '',
    address       TEXT NOT NULL DEFAULT '',

    sale_types    JSONB NOT NULL DEFAULT '[]',
    has_showcases BOOLEAN NOT NULL DEFAULT FALSE,
    has_tables    BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_phone ON tenants(phone);
CREATE INDEX IF NOT EXISTS idx_tenants_deleted_at ON tenants(deleted_at);

CREATE TABLE IF NOT EXISTS employees (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,

    tenant_id     BIGINT NOT NULL REFERENCES tenants(id),
    name          TEXT NOT NULL,
    phone         TEXT,
    role          TEXT NOT NULL DEFAULT 'cashier',
    password_hash TEXT NOT NULL,
    is_owner      BOOLEAN DEFAULT FALSE,
    is_active     BOOLEAN DEFAULT TRUE
);

CREATE INDEX IF NOT EXISTS idx_employees_tenant_id ON employees(tenant_id);
CREATE INDEX IF NOT EXISTS idx_employees_phone ON employees(phone);
CREATE INDEX IF NOT EXISTS idx_employees_deleted_at ON employees(deleted_at);

CREATE TABLE IF NOT EXISTS products (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,

    tenant_id    BIGINT NOT NULL REFERENCES tenants(id),
    name         TEXT NOT NULL,
    price        DOUBLE PRECISION NOT NULL,
    stock        INT DEFAULT 0,
    barcode      TEXT,
    category_id  BIGINT,
    image_url    TEXT,
    is_available BOOLEAN DEFAULT TRUE
);

CREATE INDEX IF NOT EXISTS idx_products_tenant_id ON products(tenant_id);
CREATE INDEX IF NOT EXISTS idx_products_barcode ON products(barcode);
CREATE INDEX IF NOT EXISTS idx_products_deleted_at ON products(deleted_at);

CREATE TABLE IF NOT EXISTS sales (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,

    tenant_id      BIGINT NOT NULL REFERENCES tenants(id),
    total          DOUBLE PRECISION NOT NULL,
    payment_method TEXT NOT NULL DEFAULT 'cash'
);

CREATE INDEX IF NOT EXISTS idx_sales_tenant_id ON sales(tenant_id);
CREATE INDEX IF NOT EXISTS idx_sales_deleted_at ON sales(deleted_at);

CREATE TABLE IF NOT EXISTS sale_items (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    sale_id    BIGINT NOT NULL REFERENCES sales(id),
    product_id BIGINT NOT NULL,
    name       TEXT NOT NULL,
    price      DOUBLE PRECISION NOT NULL,
    quantity   INT NOT NULL DEFAULT 1,
    subtotal   DOUBLE PRECISION NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sale_items_sale_id ON sale_items(sale_id);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    tenant_id  BIGINT NOT NULL REFERENCES tenants(id),
    token      VARCHAR(64) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked    BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_refresh_tokens_token ON refresh_tokens(token);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_tenant_id ON refresh_tokens(tenant_id);

-- +goose Down

DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS sale_items;
DROP TABLE IF EXISTS sales;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS employees;
DROP TABLE IF EXISTS tenants;
