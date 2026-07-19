// Spec: specs/106-onboarding-conversacional-agente/spec.md
package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/models"
)

// helper: advance assuming no model call is needed; fails the test otherwise.
func mustAdvance(t *testing.T, phase string, p models.AgentProfile, in AgentTurnInput) AgentTurn {
	t.Helper()
	out := AdvanceAgent(phase, p, in)
	require.Empty(t, out.NeedsModel, "unexpected model call needed in phase %s", phase)
	return out
}

func extractionFor(types []models.AgentTypeGuess, attrs map[string]*bool) *AgentExtraction {
	return &AgentExtraction{Types: types, Attrs: attrs}
}

func TestAgentHappyPathTiendaLicores(t *testing.T) {
	// ask_name → greeting already emitted by session start; tendero names it.
	out := mustAdvance(t, AgentPhaseAskName, models.AgentProfile{},
		AgentTurnInput{Text: "La Esquina de Don Pedro"})
	assert.Equal(t, AgentPhaseAskDescription, out.Phase)
	assert.Equal(t, "La Esquina de Don Pedro", out.Profile.BusinessName)

	// ask_description with free text → the machine asks for a model call.
	needs := AdvanceAgent(AgentPhaseAskDescription, out.Profile,
		AgentTurnInput{Text: "tengo una tienda y vendo cerveza y aguardiente"})
	assert.Equal(t, NeedsModelDescription, needs.NeedsModel)

	// Same turn re-entered with the sanitized extraction (AC-02).
	ext := extractionFor(
		[]models.AgentTypeGuess{
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.95},
			{Key: models.BusinessTypeBar, Confidence: 0.9},
		},
		map[string]*bool{},
	)
	out = mustAdvance(t, AgentPhaseAskDescription, out.Profile,
		AgentTurnInput{Text: "tengo una tienda y vendo cerveza y aguardiente", Extraction: ext})
	assert.Equal(t, AgentPhaseConfirmTypes, out.Phase)
	require.Len(t, out.Profile.Types, 2)
	// Primary = highest confidence at position 0 (AC-16).
	assert.Equal(t, models.BusinessTypeTiendaBarrio, out.Profile.Types[0].Key)
	// Confirmation names both types in plain Spanish.
	say := strings.ToLower(strings.Join(out.Say, " "))
	assert.Contains(t, say, "tienda")
	assert.Contains(t, say, "licores")
	require.Len(t, out.Chips, 2)

	// Confirm "yes" → 18+ rule communicated automatically, never asked (AC-04).
	out = mustAdvance(t, AgentPhaseConfirmTypes, out.Profile, AgentTurnInput{ChipID: ChipYes})
	assert.Equal(t, AgentPhaseFollowUps, out.Phase)
	assert.True(t, out.Profile.Age18)
	assert.True(t, out.Profile.Age18Told)
	say = strings.ToLower(strings.Join(out.Say, " "))
	assert.Contains(t, say, "18")
	// No follow-up question about 18+ exists.
	for _, k := range out.Profile.Asked {
		assert.NotEqual(t, "licores", k)
	}

	// Answer every follow-up until the proposal (bar = food → mesas asked).
	for i := 0; i < 6 && out.Phase == AgentPhaseFollowUps; i++ {
		out = mustAdvance(t, out.Phase, out.Profile, AgentTurnInput{ChipID: ChipNo})
	}
	assert.Equal(t, AgentPhasePropose, out.Phase)
	require.NotNil(t, out.Proposal)
	// Core modules always present.
	joined := strings.Join(out.Proposal.Grid, "|")
	assert.Contains(t, joined, "Vender")
	assert.Contains(t, joined, "Catálogo 18+")
}

func TestAgentPeluqueriaDoesNotAskMesas(t *testing.T) {
	// AC-05: no food types → mesas/domicilios never asked.
	p := models.AgentProfile{
		BusinessName: "Salón Estilo",
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypePeluqueria, Confidence: 0.95},
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.7},
		},
	}
	out := mustAdvance(t, AgentPhaseConfirmTypes, p, AgentTurnInput{ChipID: ChipYes})

	askedKeys := map[string]bool{}
	for out.Phase == AgentPhaseFollowUps {
		require.NotEmpty(t, out.PendingKey)
		askedKeys[out.PendingKey] = true
		out = mustAdvance(t, out.Phase, out.Profile, AgentTurnInput{ChipID: ChipYes})
	}
	assert.False(t, askedKeys["mesas"], "mesas must not be asked without food types")
	assert.False(t, askedKeys["domicilios"], "domicilios must not be asked without food types")
	assert.True(t, askedKeys["equipo"], "peluquería must ask about staff")
	assert.True(t, askedKeys["fiado"], "retail must ask about fiado")

	// Proposal reflects services + product retail (AC-03).
	require.NotNil(t, out.Proposal)
	joined := strings.Join(out.Proposal.Grid, "|")
	assert.Contains(t, joined, "Servicios")
	assert.Contains(t, joined, "Comisiones")
}

func TestAgentFollowUpCapWithManyTypes(t *testing.T) {
	// >3 types (miscelánea real): follow-ups capped at MaxAgentFollowUps.
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypeRestaurante, Confidence: 0.9},
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.85},
			{Key: models.BusinessTypePeluqueria, Confidence: 0.8},
			{Key: models.BusinessTypeDepositoConstruccion, Confidence: 0.75},
		},
	}
	out := mustAdvance(t, AgentPhaseConfirmTypes, p, AgentTurnInput{ChipID: ChipYes})
	n := 0
	for out.Phase == AgentPhaseFollowUps {
		n++
		require.LessOrEqual(t, n, MaxAgentFollowUps, "follow-ups must be capped")
		out = mustAdvance(t, out.Phase, out.Profile, AgentTurnInput{ChipID: ChipYes})
	}
	assert.Equal(t, MaxAgentFollowUps, n)
	assert.Equal(t, AgentPhasePropose, out.Phase)
}

func TestAgentSkipsAttrsAlreadyExtracted(t *testing.T) {
	// "la gente come en mesas y hago domicilios" already answered two
	// follow-ups — the machine must not re-ask them.
	yes := true
	needs := AdvanceAgent(AgentPhaseAskDescription, models.AgentProfile{BusinessName: "Donde Chava"},
		AgentTurnInput{Text: "vendo almuerzos, la gente come en mesas y hago domicilios"})
	assert.Equal(t, NeedsModelDescription, needs.NeedsModel)
	out := mustAdvance(t, AgentPhaseAskDescription, models.AgentProfile{BusinessName: "Donde Chava"},
		AgentTurnInput{
			Text: "vendo almuerzos, la gente come en mesas y hago domicilios",
			Extraction: extractionFor(
				[]models.AgentTypeGuess{{Key: models.BusinessTypeRestaurante, Confidence: 0.95}},
				map[string]*bool{"mesas": &yes, "domicilios": &yes},
			),
		})
	assert.True(t, out.Profile.Attrs["mesas"])
	out = mustAdvance(t, AgentPhaseConfirmTypes, out.Profile, AgentTurnInput{ChipID: ChipYes})
	for out.Phase == AgentPhaseFollowUps {
		assert.NotEqual(t, "mesas", out.PendingKey)
		assert.NotEqual(t, "domicilios", out.PendingKey)
		out = mustAdvance(t, out.Phase, out.Profile, AgentTurnInput{ChipID: ChipNo})
	}
	require.NotNil(t, out.Proposal)
	assert.Contains(t, strings.Join(out.Proposal.Grid, "|"), "Mesas")
}

func TestAgentEmptyDescriptionRetriesThenFallback(t *testing.T) {
	// 1st y 2nd description with zero detected types → re-ask with examples;
	// after the 2nd strike the machine offers the manual fallback (spec §9).
	p := models.AgentProfile{BusinessName: "X"}
	out := mustAdvance(t, AgentPhaseAskDescription, p,
		AgentTurnInput{Text: "pues vendo cositas", Extraction: extractionFor(nil, nil)})
	assert.Equal(t, AgentPhaseAskDescription, out.Phase)
	assert.Equal(t, 1, out.Profile.DescriptionAttempts)
	assert.False(t, out.OfferFallback)

	out = mustAdvance(t, AgentPhaseAskDescription, out.Profile,
		AgentTurnInput{Text: "de todo un poquito", Extraction: extractionFor(nil, nil)})
	assert.Equal(t, 2, out.Profile.DescriptionAttempts)
	assert.True(t, out.OfferFallback, "after 2 strikes the fallback must be offered")
}

func TestAgentCorrectionMarksCorrected(t *testing.T) {
	// AC-06: rejecting the interpretation re-opens the description and marks
	// the profile corrected (the session becomes 'corrected' on confirm).
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: models.BusinessTypeBar, Confidence: 0.9}},
	}
	out := mustAdvance(t, AgentPhaseConfirmTypes, p, AgentTurnInput{ChipID: ChipNo})
	assert.Equal(t, AgentPhaseAskDescription, out.Phase)
	assert.True(t, out.Profile.Corrected)
	assert.Empty(t, out.Profile.Types, "types reset so the new description re-interprets")
}

func TestAgentYesNoDeterministicParse(t *testing.T) {
	cases := []struct {
		text string
		want *bool
	}{
		{"sí", boolPtr(true)},
		{"si claro", boolPtr(true)},
		{"De una", boolPtr(true)},
		{"no", boolPtr(false)},
		{"no señora", boolPtr(false)},
		{"a veces", nil},
		{"depende", nil},
	}
	for _, tc := range cases {
		got := ParseYesNo(tc.text)
		if tc.want == nil {
			assert.Nil(t, got, "%q must be ambiguous", tc.text)
		} else {
			require.NotNil(t, got, "%q must parse", tc.text)
			assert.Equal(t, *tc.want, *got, "%q", tc.text)
		}
	}
}

func TestAgentAmbiguousFollowUpNeedsModelThenSingleRetry(t *testing.T) {
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: models.BusinessTypeRestaurante, Confidence: 0.9}},
	}
	out := mustAdvance(t, AgentPhaseConfirmTypes, p, AgentTurnInput{ChipID: ChipYes})
	require.Equal(t, AgentPhaseFollowUps, out.Phase)
	pending := out.PendingKey

	// Ambiguous text → model call requested.
	needs := AdvanceAgent(AgentPhaseFollowUps, out.Profile, AgentTurnInput{Text: "a veces"})
	assert.Equal(t, NeedsModelYesNo, needs.NeedsModel)

	// Model says unclear → single re-ask of the SAME question.
	unclear := "unclear"
	out2 := mustAdvance(t, AgentPhaseFollowUps, out.Profile,
		AgentTurnInput{Text: "a veces", YesNoAnswer: &unclear})
	assert.Equal(t, AgentPhaseFollowUps, out2.Phase)
	assert.Equal(t, pending, out2.PendingKey, "same question re-asked")
	assert.True(t, out2.Profile.UnclearRetry)

	// Second unclear → default No and move on (never an infinite loop).
	out3 := mustAdvance(t, AgentPhaseFollowUps, out2.Profile,
		AgentTurnInput{Text: "mmm depende", YesNoAnswer: &unclear})
	answered, ok := out3.Profile.Attrs[pending]
	require.True(t, ok, "attr must be answered after the retry")
	assert.False(t, answered)
}

func TestAgentAdjustmentKeywords(t *testing.T) {
	// FR-07: "quite el fiado" / "agregue mesas" tweak the proposal.
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.9}},
		Attrs: map[string]bool{"fiado": true},
	}
	out := mustAdvance(t, AgentPhasePropose, p, AgentTurnInput{ChipID: ChipAdjust})
	assert.True(t, out.Profile.Adjusting)

	out = mustAdvance(t, AgentPhasePropose, out.Profile, AgentTurnInput{Text: "quite el fiado"})
	assert.False(t, out.Profile.Attrs["fiado"])
	require.NotNil(t, out.Proposal)
	assert.NotContains(t, strings.Join(out.Proposal.Grid, "|"), "fiados")

	out = mustAdvance(t, AgentPhasePropose, out.Profile, AgentTurnInput{ChipID: ChipAdjust})
	out = mustAdvance(t, AgentPhasePropose, out.Profile, AgentTurnInput{Text: "agregue mesas"})
	assert.True(t, out.Profile.Attrs["mesas"])
	assert.Contains(t, strings.Join(out.Proposal.Grid, "|"), "Mesas")
}

func TestAgentProposalModules(t *testing.T) {
	p := models.AgentProfile{
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypePeluqueria, Confidence: 0.95},
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.8},
		},
		Attrs: map[string]bool{"equipo": true, "fiado": true},
	}
	prop := BuildAgentProposal(p)
	grid := strings.Join(prop.Grid, "|")
	for _, m := range []string{"Vender", "Productos", "Historial", "Ganancias", "Servicios", "Comisiones"} {
		assert.Contains(t, grid, m)
	}
	assert.NotEmpty(t, prop.Reel)
}

func boolPtr(b bool) *bool { return &b }
