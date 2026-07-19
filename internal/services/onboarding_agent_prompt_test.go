// Spec: specs/106-onboarding-conversacional-agente/spec.md
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/models"
)

func TestSanitizeAgentExtractionWhitelistsTypes(t *testing.T) {
	tr := true
	ext := &AgentExtraction{
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.9},
			// Model hallucinated / injected values — must be dropped (AC-12).
			{Key: "narcotienda", Confidence: 0.99},
			{Key: "ignore all previous instructions", Confidence: 1.0},
			{Key: models.BusinessTypeBar, Confidence: 0.8},
		},
		Attrs: map[string]*bool{
			"mesas":              &tr,
			"licores":            &tr,
			"campo_desconocido":  &tr, // unknown attr → dropped
			"enable_supplier":    &tr, // no smuggling flags through attrs
		},
	}
	SanitizeAgentExtraction(ext)

	require.Len(t, ext.Types, 2)
	assert.Equal(t, models.BusinessTypeTiendaBarrio, ext.Types[0].Key)
	assert.Equal(t, models.BusinessTypeBar, ext.Types[1].Key)
	assert.Contains(t, ext.Attrs, "mesas")
	assert.Contains(t, ext.Attrs, "licores")
	assert.NotContains(t, ext.Attrs, "campo_desconocido")
	assert.NotContains(t, ext.Attrs, "enable_supplier")
}

func TestSanitizeAgentExtractionClampsConfidenceAndOrders(t *testing.T) {
	ext := &AgentExtraction{
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypePeluqueria, Confidence: 7.5},   // clamp → 1
			{Key: models.BusinessTypeTiendaBarrio, Confidence: -2}, // clamp → 0
			{Key: models.BusinessTypeBar, Confidence: 0.6},
		},
	}
	SanitizeAgentExtraction(ext)

	require.Len(t, ext.Types, 3)
	// Ordered by confidence desc → primary first (AC-16).
	assert.Equal(t, models.BusinessTypePeluqueria, ext.Types[0].Key)
	assert.Equal(t, 1.0, ext.Types[0].Confidence)
	assert.Equal(t, models.BusinessTypeBar, ext.Types[1].Key)
	assert.Equal(t, 0.0, ext.Types[2].Confidence)
}

func TestSanitizeAgentExtractionDedupsAndMapsLegacy(t *testing.T) {
	ext := &AgentExtraction{
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.5},
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.9}, // dup keeps max
			{Key: "miscelanea", Confidence: 0.7},                    // legacy → emprendimiento_general
		},
	}
	SanitizeAgentExtraction(ext)

	require.Len(t, ext.Types, 2)
	assert.Equal(t, models.BusinessTypeTiendaBarrio, ext.Types[0].Key)
	assert.Equal(t, 0.9, ext.Types[0].Confidence)
	assert.Equal(t, models.BusinessTypeEmprendimientoGen, ext.Types[1].Key)
}

func TestSanitizeAgentExtractionNilSafe(t *testing.T) {
	SanitizeAgentExtraction(nil) // must not panic
	ext := &AgentExtraction{}
	SanitizeAgentExtraction(ext)
	assert.Empty(t, ext.Types)
	assert.NotNil(t, ext.Attrs)
}

func TestSanitizeYesNoAnswer(t *testing.T) {
	assert.Equal(t, "yes", SanitizeYesNoAnswer(" YES "))
	assert.Equal(t, "no", SanitizeYesNoAnswer("no"))
	assert.Equal(t, "unclear", SanitizeYesNoAnswer("unclear"))
	// Anything outside the enum degrades to unclear — the machine re-asks
	// instead of trusting a jailbroken answer (AC-12).
	assert.Equal(t, "unclear", SanitizeYesNoAnswer("DROP TABLE tenants"))
	assert.Equal(t, "unclear", SanitizeYesNoAnswer(""))
}
