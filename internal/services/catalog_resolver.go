// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
//
// Resolución de visibilidad de módulos para una tienda. Función PURA
// (sin DB, sin red) para que sea testeable y para que el cálculo sea
// idéntico en el preview del admin y, conceptualmente, en la app.
//
// Precedencia (spec §7, FR-08, D4):
//   1. Override de la tienda (active → en grid; inactive → oculto).
//   2. Requiere Pro y la tienda no es Pro → oculto (premium manda).
//   3. Módulo inactivo/archivado globalmente → oculto.
//   4. Relación con el/los tipo(s) de la tienda:
//        implicit            → en grid (siempre activo).
//        suggested/available → en grid si la capacidad está activada por la
//                              tienda; si no, descubrible (reel).
//        core (sin capability_key) → siempre en grid.

package services

import "vendia-backend/internal/models"

// ResolveInput agrupa todo lo necesario para resolver, sin tocar la DB.
type ResolveInput struct {
	Modules     []models.BusinessModule
	Relations   []models.ModuleTypeRelation
	Overrides   []models.TenantModuleOverride
	TenantTypes []string
	// CapabilityState: capability_key → activado por la tienda (banderas
	// enable_* del tenant). Un módulo opt-in se muestra en el grid solo si
	// su capacidad está en true (D1/D2).
	CapabilityState map[string]bool
	IsPro           bool
}

// ResolvedModule es el veredicto para un módulo en una tienda concreta.
type ResolvedModule struct {
	Module models.BusinessModule `json:"module"`
	// InGrid: aparece como tarjeta activa del dashboard.
	InGrid bool `json:"in_grid"`
	// InReel: descubrible pero no activado (sección "descubre más").
	InReel bool `json:"in_reel"`
}

// relationRank ordena los niveles para elegir el más fuerte cuando una
// tienda tiene varios tipos (implicit > suggested > available).
func relationRank(level string) int {
	switch level {
	case models.RelationImplicit:
		return 3
	case models.RelationSuggested:
		return 2
	case models.RelationAvailable:
		return 1
	default:
		return 0
	}
}

// strongestRelation devuelve el nivel de relación más fuerte entre el módulo
// y cualquiera de los tipos de la tienda; "" si no hay relación con su tipo.
func strongestRelation(moduleID string, tenantTypes []string, relations []models.ModuleTypeRelation) string {
	typeSet := make(map[string]struct{}, len(tenantTypes))
	for _, t := range tenantTypes {
		typeSet[t] = struct{}{}
	}
	best := ""
	for _, r := range relations {
		if r.ModuleID != moduleID {
			continue
		}
		if _, ok := typeSet[r.BusinessTypeValue]; !ok {
			continue
		}
		if relationRank(r.RelationLevel) > relationRank(best) {
			best = r.RelationLevel
		}
	}
	return best
}

// ResolveModules aplica la precedencia y devuelve, para cada módulo NO
// archivado, si va en el grid, en el reel, o en ninguno.
func ResolveModules(in ResolveInput) []ResolvedModule {
	overrideByModule := make(map[string]string, len(in.Overrides))
	for _, o := range in.Overrides {
		overrideByModule[o.ModuleID] = o.ForcedState
	}

	out := make([]ResolvedModule, 0, len(in.Modules))
	for _, m := range in.Modules {
		if m.ArchivedAt != nil {
			continue // archivado: nunca se muestra (D6)
		}

		// 1) Override de la tienda gana sobre todo.
		if state, ok := overrideByModule[m.ID]; ok {
			out = append(out, ResolvedModule{Module: m, InGrid: state == models.OverrideActive})
			continue
		}

		// 2) Premium manda (D4).
		if m.RequiresPro && !in.IsPro {
			out = append(out, ResolvedModule{Module: m})
			continue
		}

		// 3) Inactivo global.
		if !m.Active {
			out = append(out, ResolvedModule{Module: m})
			continue
		}

		// 4) Módulo core (sin capacidad) → siempre en grid.
		if m.CapabilityKey == nil || *m.CapabilityKey == "" {
			out = append(out, ResolvedModule{Module: m, InGrid: true})
			continue
		}

		level := strongestRelation(m.ID, in.TenantTypes, in.Relations)
		if level == models.RelationImplicit {
			out = append(out, ResolvedModule{Module: m, InGrid: true})
			continue
		}

		// suggested / available / sin relación: el estado lo da la
		// capacidad activada por la tienda (D1/D2/D7).
		activated := in.CapabilityState[*m.CapabilityKey]
		out = append(out, ResolvedModule{Module: m, InGrid: activated, InReel: !activated})
	}
	return out
}
