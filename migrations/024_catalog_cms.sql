-- +goose Up
-- Migration 024: Catalog CMS & Template Engine
-- Tables for managing global catalog templates and tenant configurations.

CREATE TABLE IF NOT EXISTS catalog_templates (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR(255) NOT NULL,
    business_type VARCHAR(50) NOT NULL,
    primary_color_hex VARCHAR(7) NOT NULL DEFAULT '#000000',
    default_banner_url TEXT,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS tenant_catalog_configs (
    tenant_id UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    template_id UUID REFERENCES catalog_templates(id) ON DELETE SET NULL,
    custom_logo_url TEXT,
    is_published BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS catalog_analytics (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id UUID NOT NULL UNIQUE REFERENCES tenants(id) ON DELETE CASCADE,
    views_count INT NOT NULL DEFAULT 0,
    orders_generated INT NOT NULL DEFAULT 0,
    last_viewed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_catalog_analytics_views ON catalog_analytics(views_count DESC);

-- +goose Down
DROP TABLE IF EXISTS catalog_analytics;
DROP TABLE IF EXISTS tenant_catalog_configs;
DROP TABLE IF EXISTS catalog_templates;
