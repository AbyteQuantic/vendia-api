// Spec: specs/036-dashboard-adaptativo-onboarding/spec.md
package services

import "vendia-backend/internal/models"

// Capabilities is the set of optional business capabilities that F036's
// onboarding pre-activates from the merchant's business type.
//
// The fields split into two groups by where they are persisted:
//
//   - Tables / Services / PriceTiers / CustomerMgmt / Quotes — each maps
//     to a real enable_* column on the Tenant row. The register handler
//     writes these to the database so the Dashboard's "optional" layer
//     renders the right modules.
//   - Recipes / FurnitureJobs — have NO dedicated enable_* column; the
//     Dashboard renders Recetas / Trabajos de Muebles in its "by type"
//     layer, derived directly from business_type. They live here only so
//     the Flutter onboarding wizard, which mirrors this map, can
//     pre-check the matching checklist items.
//
// IMPORTANT: this map is the DEFAULT applied at registration only — it
// is NOT a restriction. Any capability stays activable by ANY business
// type through the normal PATCH /api/v1/store/profile flow (Spec F036
// §4.2). Do not add type-based validation that blocks a capability.
type Capabilities struct {
	// Recipes — recetas y platos. By-type module, no enable_* column.
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
	// FurnitureJobs — trabajos de muebles. By-type module, no enable_* column.
	FurnitureJobs bool
}

// DefaultCapabilitiesForType returns the capabilities pre-activated for a
// single business type at registration time (Spec F036 §4.2). An unknown
// type — including tienda_barrio / minimercado — falls back to the empty
// (core-only) set: a fast-sale shop gets zero optional modules so its
// Dashboard stays uncluttered.
func DefaultCapabilitiesForType(t string) Capabilities {
	switch t {
	case models.BusinessTypeRestaurante, models.BusinessTypeComidasRapidas:
		return Capabilities{Recipes: true, Tables: true, Services: true}
	case models.BusinessTypeBar:
		return Capabilities{Tables: true, Services: true}
	case models.BusinessTypeDepositoConstruccion:
		return Capabilities{Quotes: true, PriceTiers: true, CustomerMgmt: true}
	case models.BusinessTypeManufactura, models.BusinessTypeReparacionMuebles:
		return Capabilities{Quotes: true, CustomerMgmt: true, FurnitureJobs: true}
	case models.BusinessTypeEmprendimientoGen:
		return Capabilities{CustomerMgmt: true}
	default: // tienda_barrio, minimercado, unknown — solo core
		return Capabilities{}
	}
}

// DefaultCapabilitiesForTypes unions the defaults of every business type
// a tenant registered with. A tenant can carry several types
// (business_types is an array); the result is the OR of each type's
// capabilities — capabilities only add, never cancel each other.
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
