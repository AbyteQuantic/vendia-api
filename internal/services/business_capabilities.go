// Spec: specs/036-dashboard-adaptativo-onboarding/spec.md
// Spec: specs/037-reel-capacidades-dashboard/spec.md
package services

// Capabilities is the set of optional business capabilities the
// registration handler could pre-activate from a tenant's business type.
//
// Historical context (F036): the map used to switch on business_type and
// turn ON specific capabilities (e.g. restaurante → recetas+mesas+
// servicios). F037 reverts that — every type now resolves to the empty
// set so a new tenant lands on a minimal Dashboard and discovers extras
// from the capabilities reel.
//
// The struct stays alive — the registration handler still calls
// DefaultCapabilitiesForTypes, and a future spec could re-enable type-
// based defaults by re-populating the switch body — but every field is
// currently dead at registration time.
type Capabilities struct {
	// Recipes — recetas y platos.
	Recipes bool
	// Tables — mesas. Persisted via FeatureFlags.EnableTables.
	Tables bool
	// Services — venta de servicios. Persisted via FeatureFlags.EnableServices.
	Services bool
	// PriceTiers — precios multi-tier. Persisted via Tenant.EnablePriceTiers.
	PriceTiers bool
	// CustomerMgmt — gestión de clientes. Persisted via Tenant.EnableCustomerManagement.
	CustomerMgmt bool
	// Quotes — cotizaciones. Persisted via Tenant.EnableQuotes.
	Quotes bool
	// FurnitureJobs — trabajos de muebles.
	FurnitureJobs bool
}

// DefaultCapabilitiesForType returns the capabilities pre-activated for a
// single business type at registration time.
//
// Spec F037 §4.1: every type — including the legacy ones that F036
// pre-activated — now resolves to the empty (core-only) set. The
// merchant arrives at a minimal Dashboard and turns capabilities on
// through the reel + BusinessCapabilitiesScreen. The function stays as
// the single seam where type-based defaults would land if we ever need
// them again.
func DefaultCapabilitiesForType(_ string) Capabilities {
	return Capabilities{}
}

// DefaultCapabilitiesForTypes unions the defaults of every business type
// a tenant registered with. Under F037 every per-type default is empty,
// so the union is always Capabilities{}; the function is kept so callers
// don't need to special-case the empty input.
func DefaultCapabilitiesForTypes(types []string) Capabilities {
	var out Capabilities
	for _, t := range types {
		c := DefaultCapabilitiesForType(t)
		out.Recipes = out.Recipes || c.Recipes
		out.Tables = out.Tables || c.Tables
		out.Services = out.Services || c.Services
		out.PriceTiers = out.PriceTiers || c.PriceTiers
		out.CustomerMgmt = out.CustomerMgmt || c.CustomerMgmt
		out.Quotes = out.Quotes || c.Quotes
		out.FurnitureJobs = out.FurnitureJobs || c.FurnitureJobs
	}
	return out
}
