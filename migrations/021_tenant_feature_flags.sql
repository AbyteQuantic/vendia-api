-- +goose Up
-- Migration 021: Tenant.feature_flags — smart per-tenant module toggles.
--
-- Rationale:
--   Migration 020 unified the business_types taxonomy. Now we need a
--   single source of truth the frontend can read at login to decide
--   which modules to render (KDS, tables, services, fractional units,
--   tips). Hard-coding those in the Flutter client would force a new
--   release every time the matrix changes; storing them in a JSONB
--   column per tenant lets the backend recompute them (migration 022,
--   admin override, seasonal campaigns) without a client rebuild.
--
--   Shape (example for a "restaurante" tenant):
--     { "enable_tables": true, "enable_kds": true, "enable_tips": true,
--       "enable_services": false, "enable_custom_billing": false,
--       "enable_fractional_units": false }
--
--   Defaults are computed by the backend in handlers/tenant_register.go
--   via models.DefaultFeatureFlags(businessTypes). Existing tenants get
--   backfilled in-place below so no row reaches the app with a null
--   feature_flags blob.

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS feature_flags JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Backfill existing tenants using the same matrix the Go helper uses.
-- Keep this in sync with models.DefaultFeatureFlags.
UPDATE tenants
   SET feature_flags = jsonb_build_object(
       'enable_tables',           (business_types::jsonb) ?| array['restaurante','comidas_rapidas','bar'] OR has_tables,
       'enable_kds',              (business_types::jsonb) ?| array['restaurante','comidas_rapidas','bar'],
       'enable_tips',             (business_types::jsonb) ?| array['restaurante','comidas_rapidas','bar'],
       'enable_services',         (business_types::jsonb) ?| array['reparacion_muebles','manufactura','emprendimiento_general'],
       'enable_custom_billing',   (business_types::jsonb) ?| array['reparacion_muebles','manufactura','emprendimiento_general'],
       'enable_fractional_units', (business_types::jsonb) ?| array['deposito_construccion']
   )
 WHERE feature_flags = '{}'::jsonb;

INSERT INTO goose_db_version (version_id, is_applied)
SELECT 21, TRUE
WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 21);

-- +goose Down
DELETE FROM goose_db_version WHERE version_id = 21;
ALTER TABLE tenants DROP COLUMN IF EXISTS feature_flags;
