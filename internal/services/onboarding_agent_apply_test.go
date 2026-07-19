// Spec: specs/106-onboarding-conversacional-agente/spec.md
package services

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/models"
)

func profilePeluqueriaTienda() models.AgentProfile {
	return models.AgentProfile{
		BusinessName: "Salón Estilo",
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypePeluqueria, Confidence: 0.95},
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.8},
		},
		Attrs: map[string]bool{"equipo": true, "fiado": true},
	}
}

func TestBuildAgentTenantUpdatesPeluqueriaTienda(t *testing.T) {
	// AC-03 + AC-16: services AND product retail enabled; primary first.
	tenant := models.Tenant{}
	updates, err := BuildAgentTenantUpdates(tenant, profilePeluqueriaTienda())
	require.NoError(t, err)

	var types []string
	require.NoError(t, json.Unmarshal([]byte(updates["business_types"].(string)), &types))
	assert.Equal(t, []string{models.BusinessTypePeluqueria, models.BusinessTypeTiendaBarrio}, types)

	var flags models.FeatureFlags
	require.NoError(t, json.Unmarshal([]byte(updates["feature_flags"].(string)), &flags))
	assert.True(t, flags.EnableServices)
	assert.True(t, flags.EnableCustomBilling)
	assert.True(t, flags.EnableStaffCommissions, "equipo=true → comisiones")

	assert.Equal(t, true, updates["enable_fiados"])
	assert.Equal(t, true, updates["onboarding_completed"])
	assert.Equal(t, "Salón Estilo", updates["business_name"])
}

func TestBuildAgentTenantUpdatesFoodWithTables(t *testing.T) {
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: models.BusinessTypeRestaurante, Confidence: 0.9}},
		Attrs: map[string]bool{"mesas": true, "domicilios": true},
	}
	updates, err := BuildAgentTenantUpdates(models.Tenant{}, p)
	require.NoError(t, err)

	var flags models.FeatureFlags
	require.NoError(t, json.Unmarshal([]byte(updates["feature_flags"].(string)), &flags))
	assert.True(t, flags.EnableTables)
	assert.Equal(t, true, updates["has_tables"])
	assert.Equal(t, true, updates["is_delivery_open"])
	assert.Equal(t, true, updates["enable_recipes"], "food → menú y recetas")
	// KDS/Tips stay minimal (F037): discovered later via the reel.
	assert.False(t, flags.EnableKDS)
	assert.False(t, flags.EnableTips)
}

func TestBuildAgentTenantUpdatesUnansweredAttrsUntouched(t *testing.T) {
	// fiado unanswered → column keeps its default (true); no key emitted.
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.9}},
	}
	updates, err := BuildAgentTenantUpdates(models.Tenant{}, p)
	require.NoError(t, err)
	_, hasFiados := updates["enable_fiados"]
	assert.False(t, hasFiados)
	_, hasDelivery := updates["is_delivery_open"]
	assert.False(t, hasDelivery)
}

func TestBuildAgentTenantUpdatesPreservesExistingFlags(t *testing.T) {
	// The apply must START from current flags (leak fix philosophy): a flag
	// already ON that Vendi doesn't manage stays ON.
	tenant := models.Tenant{FeatureFlags: models.FeatureFlags{EnableWaiterCharge: true, EnableEvents: true}}
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.9}},
	}
	updates, err := BuildAgentTenantUpdates(tenant, p)
	require.NoError(t, err)

	var flags models.FeatureFlags
	require.NoError(t, json.Unmarshal([]byte(updates["feature_flags"].(string)), &flags))
	assert.True(t, flags.EnableWaiterCharge)
	assert.True(t, flags.EnableEvents)
}

func TestBuildAgentTenantUpdatesBarNoAgeColumn(t *testing.T) {
	// 18+ is per-product and fail-closed (Specs 063/103) — the apply must
	// NOT invent a tenant column for it (spec §8).
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: models.BusinessTypeBar, Confidence: 0.9}},
		Age18: true,
	}
	updates, err := BuildAgentTenantUpdates(models.Tenant{}, p)
	require.NoError(t, err)
	for k := range updates {
		assert.NotContains(t, k, "age")
	}
}

func TestBuildAgentTenantUpdatesSupplierAndAcademy(t *testing.T) {
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypeProveedorMayorista, Confidence: 0.9},
			{Key: models.BusinessTypeAcademias, Confidence: 0.8},
		},
		Attrs: map[string]bool{"granel": true},
	}
	updates, err := BuildAgentTenantUpdates(models.Tenant{}, p)
	require.NoError(t, err)

	var flags models.FeatureFlags
	require.NoError(t, json.Unmarshal([]byte(updates["feature_flags"].(string)), &flags))
	assert.True(t, flags.EnableSupplierMode, "identidad proveedor sí se deriva del tipo (Spec 075)")
	assert.True(t, flags.EnableEvents, "academias implica eventos (F042)")
	assert.True(t, flags.EnableFractionalUnits)
}

func TestBuildAgentTenantUpdatesRejectsInvalidType(t *testing.T) {
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: "tipo_invalido", Confidence: 0.9}},
	}
	_, err := BuildAgentTenantUpdates(models.Tenant{}, p)
	assert.Error(t, err)
}

func TestBuildAgentTenantUpdatesNoTypesKeepsExisting(t *testing.T) {
	// Resumed/fallback edge: empty profile types → don't wipe existing ones.
	tenant := models.Tenant{BusinessTypes: []string{models.BusinessTypeTiendaBarrio}}
	updates, err := BuildAgentTenantUpdates(tenant, models.AgentProfile{})
	require.NoError(t, err)
	_, hasTypes := updates["business_types"]
	assert.False(t, hasTypes)
	assert.Equal(t, true, updates["onboarding_completed"])
}
