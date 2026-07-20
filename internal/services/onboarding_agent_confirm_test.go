// Spec: specs/106-onboarding-conversacional-agente/spec.md (Adenda A)
package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/models"
)

func confirmVia(t *testing.T, types []models.AgentTypeGuess) AgentTurn {
	t.Helper()
	p := models.AgentProfile{Attrs: map[string]bool{}}
	ext := &AgentExtraction{Types: types, Attrs: map[string]*bool{}}
	out := mustAdvance(t, AgentPhaseAskDescription, p,
		AgentTurnInput{Text: "descripción del negocio", Extraction: ext})
	require.Equal(t, AgentPhaseConfirmTypes, out.Phase)
	require.NotEmpty(t, out.Say)
	return out
}

// AC-A1: primario = identidad, secundarios = actividad; nunca segundo negocio.
func TestConfirmSayPeluqueriaConProductos(t *testing.T) {
	out := confirmVia(t, []models.AgentTypeGuess{
		{Key: models.BusinessTypePeluqueria, Confidence: 0.95},
		{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.7},
	})
	say := strings.ToLower(strings.Join(out.Say, " "))
	assert.Contains(t, say, "su negocio es una", "primary type reads as identity")
	assert.Contains(t, say, "peluquería")
	assert.Contains(t, say, "vende productos", "secondary reads as activity verb")
	assert.NotContains(t, say, "tienda de barrio",
		"internal taxonomy label must not leak as a second business")
	require.Len(t, out.Chips, 2)
}

// AC-A2: guardián tienda+licores — sigue nombrando tienda y licores.
func TestConfirmSayTiendaLicoresKeepsWords(t *testing.T) {
	out := confirmVia(t, []models.AgentTypeGuess{
		{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.95},
		{Key: models.BusinessTypeBar, Confidence: 0.9},
	})
	say := strings.ToLower(strings.Join(out.Say, " "))
	assert.Contains(t, say, "tienda")
	assert.Contains(t, say, "licores")
}

// Un solo tipo: identidad simple, sin frase de actividad.
func TestConfirmSaySingleType(t *testing.T) {
	out := confirmVia(t, []models.AgentTypeGuess{
		{Key: models.BusinessTypeRestaurante, Confidence: 0.9},
	})
	say := strings.ToLower(strings.Join(out.Say, " "))
	assert.Contains(t, say, "su negocio es un")
	assert.Contains(t, say, "restaurante")
	assert.NotContains(t, say, "además")
}

// AC-A3: toda clave de la taxonomía tiene identidad y actividad propias —
// sin fallbacks torpes tipo "que además minimercado".
func TestConfirmMapsCoverWholeTaxonomy(t *testing.T) {
	for key := range agentTypeLabels {
		assert.Contains(t, agentTypeIdentity, key, "missing identity phrase for %s", key)
		assert.Contains(t, agentTypeActivityPhrase, key, "missing activity phrase for %s", key)
	}
}

// Idempotencia: re-entrada por texto libre desde confirm_types produce el
// mismo formato (la corrección re-usa el mismo camino).
func TestConfirmSayIdempotentOnReentry(t *testing.T) {
	types := []models.AgentTypeGuess{
		{Key: models.BusinessTypePeluqueria, Confidence: 0.95},
		{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.7},
	}
	first := confirmVia(t, types)
	again := mustAdvance(t, AgentPhaseAskDescription, first.Profile,
		AgentTurnInput{Text: "lo mismo con otras palabras",
			Extraction: &AgentExtraction{Types: types, Attrs: map[string]*bool{}}})
	assert.Equal(t, first.Say, again.Say)
}
