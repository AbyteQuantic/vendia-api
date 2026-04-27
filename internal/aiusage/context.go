package aiusage

import "context"

type ctxKey int

const tenantIDKey ctxKey = 1

// WithTenantID attaches a tenant_id for AI usage logging (must be
// set before calling Gemini from authenticated tenant routes).
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	if tenantID == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

// TenantIDFromContext returns the tenant_id previously attached, or "".
func TenantIDFromContext(ctx context.Context) string {
	v, ok := ctx.Value(tenantIDKey).(string)
	if !ok {
		return ""
	}
	return v
}
