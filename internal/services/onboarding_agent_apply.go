// Spec: specs/106-onboarding-conversacional-agente/spec.md
//
// Materializes a confirmed Vendi profile onto the Tenant. Philosophy (leak
// fix, plan §6): START from the tenant's CURRENT flags and only touch what
// the conversation explicitly decided — never re-derive the whole matrix from
// business types, so nothing the tendero (or Vendi) turned off flips back on.
package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"vendia-backend/internal/models"
)

// BuildAgentTenantUpdates converts a session profile into the column updates
// for the tenants table. Pure function (unit-testable without a DB); the
// handler wraps it in a transaction. Never emits a key it has no opinion on —
// unanswered attributes keep their column defaults (spec §7).
func BuildAgentTenantUpdates(tenant models.Tenant, p models.AgentProfile) (map[string]any, error) {
	updates := map[string]any{"onboarding_completed": true}

	types := make([]string, 0, len(p.Types))
	for _, tg := range p.Types {
		key := strings.TrimSpace(tg.Key)
		if _, ok := models.ValidBusinessTypes[key]; !ok {
			return nil, fmt.Errorf("tipo de negocio no válido: %q", key)
		}
		types = append(types, key)
	}
	typeSet := map[string]bool{}
	for _, t := range types {
		typeSet[t] = true
	}

	// Position 0 = primary (FR-14/AC-16); empty = keep whatever the tenant
	// already has (resumed edge case — never wipe).
	if len(types) > 0 {
		typesJSON, err := json.Marshal(types)
		if err != nil {
			return nil, fmt.Errorf("no se pudo serializar los tipos: %w", err)
		}
		updates["business_types"] = string(typesJSON)
	}

	// Flags: preserve current, then apply explicit decisions only.
	flags := tenant.FeatureFlags

	if mesas, ok := p.Attrs["mesas"]; ok {
		flags.EnableTables = mesas
	}
	if granel, ok := p.Attrs["granel"]; ok {
		flags.EnableFractionalUnits = granel
	} else if typeSet[models.BusinessTypeDepositoConstruccion] {
		// Depósito's identity includes bulk sales (proposal shows it).
		flags.EnableFractionalUnits = true
	}
	if equipo, ok := p.Attrs["equipo"]; ok {
		flags.EnableStaffCommissions = equipo
	}
	if typeSet[models.BusinessTypePeluqueria] {
		// Confirmed services identity → the Servicios module the proposal
		// promised (AC-03). KDS/Tips stay OFF (F037 minimal — reel later).
		flags.EnableServices = true
		flags.EnableCustomBilling = true
	}
	if typeSet[models.BusinessTypeAcademias] {
		// Academias implies Eventos (F042) — same rule as the profile PATCH.
		flags.EnableEvents = true
	}
	if typeSet[models.BusinessTypeProveedorAgricola] || typeSet[models.BusinessTypeProveedorMayorista] {
		// Supplier identity IS type-derived (Spec 075) — mirror the register.
		flags.EnableSupplierMode = true
	}

	flagsJSON, err := json.Marshal(flags)
	if err != nil {
		return nil, fmt.Errorf("no se pudo serializar las capacidades: %w", err)
	}
	updates["feature_flags"] = string(flagsJSON)
	updates["has_tables"] = flags.EnableTables

	if fiado, ok := p.Attrs["fiado"]; ok {
		updates["enable_fiados"] = fiado
	}
	if domicilios, ok := p.Attrs["domicilios"]; ok {
		updates["is_delivery_open"] = domicilios
	}
	if isFood(typeSet) {
		// "Menú y recetas" promised in the proposal for food businesses.
		updates["enable_recipes"] = true
	}

	if name := strings.TrimSpace(p.BusinessName); name != "" {
		updates["business_name"] = name
	}

	// NOTE: no 18+ column exists on Tenant on purpose — the per-product
	// IsAgeRestricted gates (Specs 063/103) are fail-closed platform-wide.
	// p.Age18 lives in the session profile as the communicated record.
	return updates, nil
}

// AgentSessionFinalStatus resolves the terminal status for a confirmed
// session: corrected sessions are the highest-value training data (FR-09).
func AgentSessionFinalStatus(p models.AgentProfile) string {
	if p.Corrected {
		return models.AgentSessionStatusCorrected
	}
	return models.AgentSessionStatusConfirmed
}
