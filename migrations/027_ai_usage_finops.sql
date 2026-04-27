-- +goose Up
-- Telemetría Gemini (FinOps) y pagos de suscripción para el panel Super Admin.
-- La app también AutoMigrate estos modelos; el SQL queda como referencia.

CREATE TABLE IF NOT EXISTS ai_usage_logs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL,
  feature VARCHAR(32) NOT NULL,
  tokens_input BIGINT NOT NULL DEFAULT 0,
  tokens_output BIGINT NOT NULL DEFAULT 0,
  estimated_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
  model_name VARCHAR(128) NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ai_usage_logs_tenant ON ai_usage_logs (tenant_id);
CREATE INDEX IF NOT EXISTS idx_ai_usage_logs_feature ON ai_usage_logs (feature);
CREATE INDEX IF NOT EXISTS idx_ai_usage_logs_created ON ai_usage_logs (created_at);

CREATE TABLE IF NOT EXISTS subscription_payments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL,
  amount_usd DECIMAL(14,4) NOT NULL,
  status VARCHAR(32) NOT NULL,
  external_ref VARCHAR(256) NOT NULL DEFAULT '',
  confirmed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_sub_pay_tenant ON subscription_payments (tenant_id);
CREATE INDEX IF NOT EXISTS idx_sub_pay_status ON subscription_payments (status);
CREATE INDEX IF NOT EXISTS idx_sub_pay_created_at ON subscription_payments (created_at);
CREATE INDEX IF NOT EXISTS idx_sub_pay_confirmed_at ON subscription_payments (confirmed_at);

-- +goose Down
DROP TABLE IF EXISTS subscription_payments;
DROP TABLE IF EXISTS ai_usage_logs;
