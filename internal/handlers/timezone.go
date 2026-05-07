package handlers

import "time"

// tenantTZ is the canonical business timezone for all VendIA tenants.
// Colombia uses UTC-5 year-round (no DST), so a FixedZone is sufficient
// and avoids depending on tzdata being present in the runtime image.
//
// When the product expands beyond Colombia, replace this constant with
// a per-tenant lookup driven by a tenant.timezone column.
var tenantTZ = time.FixedZone("America/Bogota", -5*60*60)

// tenantNow returns the current wall-clock time as it would read on a
// merchant's phone in Colombia. Use this anywhere a handler builds a
// "today" boundary — time.Now() on its own returns UTC on Render, which
// silently shifts the cutoff by 5 hours.
func tenantNow() time.Time {
	return time.Now().In(tenantTZ)
}

// startOfTenantDay returns midnight at the start of the calendar day
// that contains [t], expressed in the tenant timezone. The returned
// time.Time still encodes the absolute instant (epoch is correct), so
// it can be used directly in GORM .Where("created_at >= ?", ...) calls.
func startOfTenantDay(t time.Time) time.Time {
	local := t.In(tenantTZ)
	return time.Date(
		local.Year(), local.Month(), local.Day(),
		0, 0, 0, 0,
		tenantTZ,
	)
}
