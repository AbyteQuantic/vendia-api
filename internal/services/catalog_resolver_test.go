// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"vendia-backend/internal/models"
)

func strptr(s string) *string { return &s }

// mod arma un módulo de prueba con una capacidad opcional.
func mod(id, capKey string) models.BusinessModule {
	m := models.BusinessModule{Name: id, Active: true, Category: models.CategoryVender}
	m.ID = id
	if capKey != "" {
		m.CapabilityKey = strptr(capKey)
	}
	return m
}

func findResolved(rs []ResolvedModule, id string) ResolvedModule {
	for _, r := range rs {
		if r.Module.ID == id {
			return r
		}
	}
	return ResolvedModule{}
}

func TestResolveModules_CorePinnedToGrid(t *testing.T) {
	core := mod("registrar_venta", "") // sin capacidad
	out := ResolveModules(ResolveInput{Modules: []models.BusinessModule{core}})
	r := findResolved(out, "registrar_venta")
	assert.True(t, r.InGrid, "un módulo core siempre va en el grid")
	assert.False(t, r.InReel)
}

func TestResolveModules_ImplicitByType(t *testing.T) {
	m := mod("quotes", "enable_quotes")
	rel := models.ModuleTypeRelation{ModuleID: "quotes", BusinessTypeValue: "ferreteria", RelationLevel: models.RelationImplicit}
	out := ResolveModules(ResolveInput{
		Modules:     []models.BusinessModule{m},
		Relations:   []models.ModuleTypeRelation{rel},
		TenantTypes: []string{"ferreteria"},
		// Sin capacidad activada: implícito igual lo pone en grid.
		CapabilityState: map[string]bool{},
	})
	assert.True(t, findResolved(out, "quotes").InGrid, "implícito → grid sin importar la bandera")
}

func TestResolveModules_AvailableActivatedVsDiscoverable(t *testing.T) {
	m := mod("recetas", "enable_recipes")
	base := ResolveInput{Modules: []models.BusinessModule{m}, TenantTypes: []string{"tienda_barrio"}}

	// Activada por la tienda → grid.
	on := base
	on.CapabilityState = map[string]bool{"enable_recipes": true}
	assert.True(t, findResolved(ResolveModules(on), "recetas").InGrid)

	// No activada → descubrible (reel), no en grid.
	off := base
	off.CapabilityState = map[string]bool{}
	r := findResolved(ResolveModules(off), "recetas")
	assert.False(t, r.InGrid)
	assert.True(t, r.InReel)
}

func TestResolveModules_OverrideWinsOverGlobalInactive(t *testing.T) {
	m := mod("promos", "enable_promotions")
	m.Active = false // inactivo global
	ov := models.TenantModuleOverride{ModuleID: "promos", ForcedState: models.OverrideActive}
	out := ResolveModules(ResolveInput{
		Modules:   []models.BusinessModule{m},
		Overrides: []models.TenantModuleOverride{ov},
	})
	assert.True(t, findResolved(out, "promos").InGrid, "override active gana sobre inactivo global")
}

func TestResolveModules_OverrideInactiveHidesImplicit(t *testing.T) {
	m := mod("quotes", "enable_quotes")
	rel := models.ModuleTypeRelation{ModuleID: "quotes", BusinessTypeValue: "ferreteria", RelationLevel: models.RelationImplicit}
	ov := models.TenantModuleOverride{ModuleID: "quotes", ForcedState: models.OverrideInactive}
	out := ResolveModules(ResolveInput{
		Modules:     []models.BusinessModule{m},
		Relations:   []models.ModuleTypeRelation{rel},
		Overrides:   []models.TenantModuleOverride{ov},
		TenantTypes: []string{"ferreteria"},
	})
	r := findResolved(out, "quotes")
	assert.False(t, r.InGrid, "override inactive oculta aunque el tipo lo conceda")
	assert.False(t, r.InReel)
}

func TestResolveModules_PremiumGating(t *testing.T) {
	m := mod("marketing", "enable_marketing_hub")
	m.RequiresPro = true
	m.CapabilityKey = strptr("enable_marketing_hub")
	in := ResolveInput{
		Modules:         []models.BusinessModule{m},
		TenantTypes:     []string{"tienda_barrio"},
		CapabilityState: map[string]bool{"enable_marketing_hub": true},
	}

	// Sin Pro → oculto aunque la capacidad esté activada.
	in.IsPro = false
	r := findResolved(ResolveModules(in), "marketing")
	assert.False(t, r.InGrid)
	assert.False(t, r.InReel)

	// Con Pro → entra al grid.
	in.IsPro = true
	assert.True(t, findResolved(ResolveModules(in), "marketing").InGrid)
}

func TestResolveModules_ArchivedExcluded(t *testing.T) {
	m := mod("viejo", "")
	now := time.Now()
	m.ArchivedAt = &now
	out := ResolveModules(ResolveInput{Modules: []models.BusinessModule{m}})
	assert.Empty(t, out, "un módulo archivado no aparece en absoluto")
}
