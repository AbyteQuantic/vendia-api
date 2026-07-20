// Spec: specs/106-onboarding-conversacional-agente/spec.md
//
// Deterministic state machine for the conversational onboarding with "Vendi".
// Go drives the conversation (phases, questions, chips, caps); the model ONLY
// interprets free text. That keeps every path unit-testable, the AI budget
// bounded, and makes it impossible for the model to invent questions or
// modules (spec §7).
package services

import (
	"fmt"
	"strings"

	"vendia-backend/internal/models"
)

// AgentName is the assistant's confirmed identity (clarificación #1).
const AgentName = "Vendi"

// Conversation phases — persisted on AgentSession.Phase so an abandoned
// session resumes exactly where it stopped (FR-11).
const (
	AgentPhaseAskName        = "ask_name"
	AgentPhaseAskDescription = "ask_description"
	AgentPhaseConfirmTypes   = "confirm_types"
	AgentPhaseFollowUps      = "follow_ups"
	AgentPhasePropose        = "propose"
	AgentPhaseDone           = "done"
)

// NeedsModel values: the machine cannot advance without an interpretation.
const (
	NeedsModelDescription = "description"
	NeedsModelYesNo       = "yesno"
)

// Chip IDs the frontend sends back verbatim.
const (
	ChipYes     = "yes"
	ChipNo      = "no"
	ChipConfirm = "confirm"
	ChipAdjust  = "adjust"
)

// MaxAgentModelCalls is the hard per-session AI budget (AC-14).
const MaxAgentModelCalls = 12

// MaxAgentFollowUps caps follow-up questions so the whole conversation stays
// ≤ 8 questions even for a miscelánea with many types (spec §5/§9).
const MaxAgentFollowUps = 4

// MaxDescriptionAttempts: descriptions with zero detected types before the
// assistant offers the manual fallback (spec §9).
const MaxDescriptionAttempts = 2

// AgentChip is one quick-reply option.
type AgentChip struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// AgentExtraction is the sanitized model output for a free-text description.
type AgentExtraction struct {
	Types []models.AgentTypeGuess `json:"types"`
	// Attrs uses canonical keys (mesas, domicilios, fiado, equipo, granel,
	// licores); nil pointer = not mentioned.
	Attrs        map[string]*bool `json:"attrs"`
	BusinessName *string          `json:"business_name"`
}

// AgentTurnInput is what a single request contributes: raw text and/or a chip,
// plus — on the re-entrant call after a model roundtrip — the interpretation.
type AgentTurnInput struct {
	Text        string
	ChipID      string
	Extraction  *AgentExtraction // set after a NeedsModelDescription roundtrip
	YesNoAnswer *string          // "yes"|"no"|"unclear" after a NeedsModelYesNo roundtrip
}

// AgentProposal is the module summary shown before confirming (FR-06).
type AgentProposal struct {
	Grid []string `json:"grid"`
	Reel []string `json:"reel"`
}

// AgentTurn is the machine's answer for one advance.
type AgentTurn struct {
	Phase      string
	Profile    models.AgentProfile
	Say        []string
	Chips      []AgentChip
	NeedsModel string
	// PendingKey is the follow-up being asked (test/observability hook).
	PendingKey string
	// OfferFallback: the UI should surface the manual type-selection path.
	OfferFallback bool
	Proposal      *AgentProposal
	Done          bool
}

// agentTypeLabels maps canonical types to tendero-friendly Spanish.
var agentTypeLabels = map[string]string{
	models.BusinessTypeTiendaBarrio:         "tienda de barrio",
	models.BusinessTypeMinimercado:          "minimercado",
	models.BusinessTypeDepositoConstruccion: "depósito / materiales",
	models.BusinessTypeRestaurante:          "restaurante",
	models.BusinessTypeComidasRapidas:       "comidas rápidas",
	models.BusinessTypeBar:                  "venta de licores / bar",
	models.BusinessTypeManufactura:          "manufactura",
	models.BusinessTypeReparacionMuebles:    "reparación de muebles",
	models.BusinessTypeEmprendimientoGen:    "emprendimiento",
	models.BusinessTypeAcademias:            "academia / instituto",
	models.BusinessTypeProveedorAgricola:    "proveedor agrícola",
	models.BusinessTypeProveedorMayorista:   "proveedor mayorista",
	models.BusinessTypePeluqueria:           "peluquería / belleza",
}

// Adenda A (spec 106): la confirmación presenta, no clasifica. El tipo
// primario se dice como IDENTIDAD (artículo + nombre que el tendero reconoce
// como SU negocio); los secundarios se traducen a ACTIVIDAD en verbo, nunca
// como un segundo negocio ("tienda de barrio" confundía a una peluquería que
// vende productos). Ningún secundario se oculta: el tendero debe poder negar
// cualquier detección — en especial licores, que activa el control 18+.
var agentTypeIdentity = map[string]string{
	models.BusinessTypeTiendaBarrio:         "una <b>tienda de barrio</b>",
	models.BusinessTypeMinimercado:          "un <b>minimercado</b>",
	models.BusinessTypeDepositoConstruccion: "un <b>depósito de materiales</b>",
	models.BusinessTypeRestaurante:          "un <b>restaurante</b>",
	models.BusinessTypeComidasRapidas:       "un negocio de <b>comidas rápidas</b>",
	models.BusinessTypeBar:                  "un <b>bar / venta de licores</b>",
	models.BusinessTypeManufactura:          "un negocio de <b>manufactura</b>",
	models.BusinessTypeReparacionMuebles:    "un taller de <b>reparación de muebles</b>",
	models.BusinessTypeEmprendimientoGen:    "un <b>emprendimiento</b>",
	models.BusinessTypeAcademias:            "una <b>academia</b>",
	models.BusinessTypeProveedorAgricola:    "un <b>proveedor agrícola</b>",
	models.BusinessTypeProveedorMayorista:   "un <b>proveedor mayorista</b>",
	models.BusinessTypePeluqueria:           "una <b>peluquería</b>",
}

var agentTypeActivityPhrase = map[string]string{
	models.BusinessTypeTiendaBarrio:         "que además vende productos",
	models.BusinessTypeMinimercado:          "que además vende productos de mercado",
	models.BusinessTypeDepositoConstruccion: "que además vende materiales de construcción",
	models.BusinessTypeRestaurante:          "que además prepara y sirve comida",
	models.BusinessTypeComidasRapidas:       "que además vende comidas rápidas",
	models.BusinessTypeBar:                  "donde también se venden licores",
	models.BusinessTypeManufactura:          "que además fabrica sus propios productos",
	models.BusinessTypeReparacionMuebles:    "que además repara muebles",
	models.BusinessTypeEmprendimientoGen:    "que además vende sus propios productos",
	models.BusinessTypeAcademias:            "que además dicta clases o cursos",
	models.BusinessTypeProveedorAgricola:    "que además surte productos del campo",
	models.BusinessTypeProveedorMayorista:   "que además vende al por mayor",
	models.BusinessTypePeluqueria:           "que además presta servicios de belleza",
}

// agentTypeReadyPhrase anuncia qué le queda listo por cada tipo secundario.
// Solo capacidades que el catálogo realmente configura (spec §7 "nunca
// inventa"); si un tipo no tiene entrada, la frase se omite.
var agentTypeReadyPhrase = map[string]string{
	models.BusinessTypeTiendaBarrio:   "el inventario para esos productos",
	models.BusinessTypeMinimercado:    "el inventario para esos productos",
	models.BusinessTypeBar:            "el control de venta de licores a mayores de edad",
	models.BusinessTypeRestaurante:    "el manejo de platos y cocina",
	models.BusinessTypeComidasRapidas: "el manejo de pedidos de comida",
}

// buildConfirmationSay es función pura de types: la re-entrada por texto
// libre desde confirm_types produce exactamente el mismo formato.
func buildConfirmationSay(types []models.AgentTypeGuess) string {
	if len(types) == 0 {
		return "¿Me confirma si eso es correcto? 🙂"
	}
	identity, ok := agentTypeIdentity[types[0].Key]
	if !ok {
		identity = agentTypeLabel(types[0].Key)
	}
	if len(types) == 1 {
		return fmt.Sprintf("Entendido: su negocio es %s. ¿Es correcto?", identity)
	}
	activities := make([]string, 0, len(types)-1)
	ready := make([]string, 0, len(types)-1)
	for _, tg := range types[1:] {
		if a, ok := agentTypeActivityPhrase[tg.Key]; ok {
			activities = append(activities, a)
		} else {
			activities = append(activities, "que además "+strings.ToLower(agentTypeLabel(tg.Key)))
		}
		if r, ok := agentTypeReadyPhrase[tg.Key]; ok {
			ready = append(ready, r)
		}
	}
	base := fmt.Sprintf("Entendido: su negocio es %s %s.", identity, strings.Join(activities, " y "))
	if len(ready) == 0 {
		return base + " ¿Es correcto?"
	}
	return fmt.Sprintf("%s Le dejo listo %s, ¿es correcto?", base, strings.Join(ready, " y "))
}

func agentTypeLabel(key string) string {
	if l, ok := agentTypeLabels[key]; ok {
		return l
	}
	return key
}

// AgentTypeLabel exposes the tendero-facing Spanish label (wire responses).
func AgentTypeLabel(key string) string { return agentTypeLabel(key) }

// AgentFollowUpQuestion returns the question text for a follow-up key —
// the handler needs it to ground the yes/no interpretation call.
func AgentFollowUpQuestion(key string) string {
	for _, fu := range agentFollowUps {
		if fu.Key == key {
			return fu.Question
		}
	}
	return ""
}

// followUpDef is one conditional question. Order in agentFollowUps = priority
// (highest grid impact first) when the cap kicks in.
type followUpDef struct {
	Key      string
	Question string
	YesLabel string
	NoLabel  string
	Applies  func(types map[string]bool) bool
}

func anyOf(types map[string]bool, keys ...string) bool {
	for _, k := range keys {
		if types[k] {
			return true
		}
	}
	return false
}

func isFood(types map[string]bool) bool {
	return anyOf(types, models.BusinessTypeRestaurante, models.BusinessTypeComidasRapidas, models.BusinessTypeBar)
}

func isRetail(types map[string]bool) bool {
	return anyOf(types,
		models.BusinessTypeTiendaBarrio, models.BusinessTypeMinimercado,
		models.BusinessTypeDepositoConstruccion, models.BusinessTypeEmprendimientoGen,
		models.BusinessTypeManufactura,
	)
}

// NOTE: no "citas/agenda" follow-up in v1 — there is no appointments
// capability in the catalog yet, and Vendi never asks what it cannot
// configure (spec §7 "nunca inventa"). FR-04's list is illustrative.
var agentFollowUps = []followUpDef{
	{
		Key:      "mesas",
		Question: "¿Sus clientes consumen en <b>mesas</b> dentro del local?",
		Applies:  isFood,
	},
	{
		Key:      "granel",
		Question: "¿Vende <b>a granel o por bultos</b> (kilos, arrobas, cajas)?",
		Applies: func(t map[string]bool) bool {
			return anyOf(t, models.BusinessTypeDepositoConstruccion,
				models.BusinessTypeProveedorMayorista, models.BusinessTypeProveedorAgricola)
		},
	},
	{
		Key:      "equipo",
		Question: "¿Trabaja usted solo o con <b>más profesionales</b>?",
		YesLabel: "Con equipo",
		NoLabel:  "Solo yo",
		Applies: func(t map[string]bool) bool {
			return t[models.BusinessTypePeluqueria]
		},
	},
	{
		Key:      "domicilios",
		Question: "¿Hace <b>domicilios</b>?",
		Applies:  isFood,
	},
	{
		Key:      "fiado",
		Question: "¿Le <b>fía</b> a clientes de confianza?",
		Applies: func(t map[string]bool) bool {
			return isRetail(t) || isFood(t)
		},
	},
}

func typeSet(p models.AgentProfile) map[string]bool {
	set := map[string]bool{}
	for _, tg := range p.Types {
		set[tg.Key] = true
	}
	return set
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// nextFollowUp returns the next applicable, unanswered, unasked question — or
// nil when the queue is exhausted or the cap is reached.
func nextFollowUp(p models.AgentProfile) *followUpDef {
	if len(p.Asked) >= MaxAgentFollowUps {
		return nil
	}
	types := typeSet(p)
	for i := range agentFollowUps {
		fu := agentFollowUps[i]
		if !fu.Applies(types) {
			continue
		}
		if _, answered := p.Attrs[fu.Key]; answered {
			continue
		}
		if contains(p.Asked, fu.Key) {
			continue
		}
		return &fu
	}
	return nil
}

// ParseYesNo resolves an unambiguous Colombian Spanish yes/no deterministically
// (no AI cost). nil = ambiguous → model roundtrip.
func ParseYesNo(text string) *bool {
	t := " " + strings.ToLower(strings.TrimSpace(text)) + " "
	yes := []string{" si ", " sí ", " claro ", " correcto ", " exacto ", " de una ", " listo ", " dale ", " obvio ", " asi es ", " así es ", " con equipo "}
	no := []string{" no ", " tampoco ", " nada ", " negativo ", " solo yo ", " sola ", " solo "}
	// "no" wins when both appear ("no, claro que no").
	for _, n := range no {
		if strings.Contains(t, n) {
			b := false
			return &b
		}
	}
	for _, y := range yes {
		if strings.Contains(t, y) {
			b := true
			return &b
		}
	}
	return nil
}

// AgentGreeting is what the assistant says when a session starts.
func AgentGreeting() []string {
	return []string{
		fmt.Sprintf("¡Hola! 👋 Soy <b>%s</b>. Voy a dejar su tienda lista en un par de minutos, solo conversando.", AgentName),
		"Primero, ¿cómo se llama su negocio?",
	}
}

// AdvanceAgent is the pure state-machine step. It never mutates its inputs:
// the returned AgentTurn carries the new profile (Art. IX).
func AdvanceAgent(phase string, prev models.AgentProfile, in AgentTurnInput) AgentTurn {
	p := cloneProfile(prev)

	switch phase {
	case AgentPhaseAskName:
		return advanceAskName(p, in)
	case AgentPhaseAskDescription:
		return advanceAskDescription(p, in)
	case AgentPhaseConfirmTypes:
		return advanceConfirmTypes(p, in)
	case AgentPhaseFollowUps:
		return advanceFollowUps(p, in)
	case AgentPhasePropose:
		return advancePropose(p, in)
	default: // done or unknown → stay done, no-op
		return AgentTurn{Phase: AgentPhaseDone, Profile: p, Done: true,
			Say: []string{"Su tienda ya está configurada. Cualquier cambio lo hace desde su panel. 💙"}}
	}
}

func cloneProfile(p models.AgentProfile) models.AgentProfile {
	out := p
	out.Types = append([]models.AgentTypeGuess(nil), p.Types...)
	out.Asked = append([]string(nil), p.Asked...)
	out.Attrs = map[string]bool{}
	for k, v := range p.Attrs {
		out.Attrs[k] = v
	}
	return out
}

func advanceAskName(p models.AgentProfile, in AgentTurnInput) AgentTurn {
	name := strings.TrimSpace(in.Text)
	if name == "" {
		return AgentTurn{Phase: AgentPhaseAskName, Profile: p,
			Say:   []string{"¿Me repite el nombre de su negocio, por favor? 🙂"},
			Chips: nil}
	}
	p.BusinessName = name
	return AgentTurn{Phase: AgentPhaseAskDescription, Profile: p, Say: []string{
		fmt.Sprintf("¡Qué buen nombre! 🎉 Ahora cuénteme con sus palabras: <b>¿qué vende o qué servicios ofrece en %s?</b>", name),
		"No importa si son varias cosas — muchos negocios venden de todo un poquito.",
	}}
}

func advanceAskDescription(p models.AgentProfile, in AgentTurnInput) AgentTurn {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return AgentTurn{Phase: AgentPhaseAskDescription, Profile: p,
			Say: []string{"Cuénteme qué vende o qué servicios ofrece. Por ejemplo: <i>«vendo almuerzos y gaseosas»</i> o <i>«arreglo uñas y vendo cremas»</i>."}}
	}
	if in.Extraction == nil {
		// The machine cannot interpret free text — ask the handler to run
		// the model, then re-enter this same phase with the extraction.
		return AgentTurn{Phase: AgentPhaseAskDescription, Profile: p, NeedsModel: NeedsModelDescription}
	}

	ext := in.Extraction
	if len(ext.Types) == 0 {
		p.DescriptionAttempts++
		turn := AgentTurn{Phase: AgentPhaseAskDescription, Profile: p, Say: []string{
			"Mmm, no le entendí muy bien 🤔 ¿Me cuenta un poco más? Por ejemplo: <i>«tengo una tienda y vendo cerveza»</i> o <i>«hago almuerzos y domicilios»</i>.",
		}}
		if p.DescriptionAttempts >= MaxDescriptionAttempts {
			turn.OfferFallback = true
			turn.Say = []string{
				"No le estoy entendiendo bien y no quiero hacerle perder tiempo. 🙏",
				"Mejor escoja usted los tipos de su negocio en la lista y seguimos de una.",
			}
		}
		return turn
	}

	// Merge: keep already-confirmed types, add new ones (corrections re-enter
	// here with Types reset, so no stale data survives a rejection).
	p.Types = mergeTypes(p.Types, ext.Types)
	for k, v := range ext.Attrs {
		if v != nil && k != "licores" {
			p.Attrs[k] = *v
		}
	}
	if alcohol, ok := ext.Attrs["licores"]; ok && alcohol != nil && *alcohol {
		p.Age18 = true
	}
	if ext.BusinessName != nil && strings.TrimSpace(*ext.BusinessName) != "" && p.BusinessName == "" {
		p.BusinessName = strings.TrimSpace(*ext.BusinessName)
	}
	p.DescriptionAttempts = 0

	return AgentTurn{Phase: AgentPhaseConfirmTypes, Profile: p,
		Say: []string{buildConfirmationSay(p.Types)},
		Chips: []AgentChip{
			{ID: ChipYes, Label: "Sí, así es"},
			{ID: ChipNo, Label: "Falta algo / no es así"},
		}}
}

// mergeTypes keeps confidence-descending order; the head is the primary type
// (FR-14 / AC-16). Duplicates keep the highest confidence.
func mergeTypes(prev, add []models.AgentTypeGuess) []models.AgentTypeGuess {
	best := map[string]float64{}
	for _, t := range prev {
		if t.Confidence > best[t.Key] {
			best[t.Key] = t.Confidence
		}
	}
	for _, t := range add {
		if t.Confidence > best[t.Key] {
			best[t.Key] = t.Confidence
		}
	}
	out := make([]models.AgentTypeGuess, 0, len(best))
	for k, c := range best {
		out = append(out, models.AgentTypeGuess{Key: k, Confidence: c})
	}
	// Insertion order from a map is random — sort by confidence desc,
	// stable tie-break by key so tests are deterministic.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Confidence > out[i].Confidence ||
				(out[j].Confidence == out[i].Confidence && out[j].Key < out[i].Key) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func advanceConfirmTypes(p models.AgentProfile, in AgentTurnInput) AgentTurn {
	answer := in.ChipID
	if answer == "" {
		if yn := ParseYesNo(in.Text); yn != nil {
			if *yn {
				answer = ChipYes
			} else {
				answer = ChipNo
			}
		} else if strings.TrimSpace(in.Text) != "" {
			// The tendero is expanding the description instead of answering —
			// treat it as a new description (needs interpretation).
			return advanceAskDescription(p, in)
		}
	}

	switch answer {
	case ChipYes:
		// Alcohol platform rule (FR-05/AC-04): bar type or explicit mention.
		if typeSet(p)[models.BusinessTypeBar] {
			p.Age18 = true
		}
		return enterFollowUps(p)
	case ChipNo:
		p.Corrected = true
		p.Types = nil
		p.Attrs = map[string]bool{}
		p.Age18 = false
		p.Age18Told = false
		return AgentTurn{Phase: AgentPhaseAskDescription, Profile: p, Say: []string{
			"No hay problema. Cuénteme de nuevo qué vende o qué hace, con otras palabras, y yo vuelvo a interpretar. 🙂",
		}}
	default:
		return AgentTurn{Phase: AgentPhaseConfirmTypes, Profile: p,
			Say: []string{"¿Me confirma si eso es correcto? 🙂"},
			Chips: []AgentChip{
				{ID: ChipYes, Label: "Sí, así es"},
				{ID: ChipNo, Label: "Falta algo / no es así"},
			}}
	}
}

// enterFollowUps emits the one-time 18+ notice and asks the next question —
// or jumps straight to the proposal when nothing applies.
func enterFollowUps(p models.AgentProfile) AgentTurn {
	say := []string{}
	if p.Age18 && !p.Age18Told {
		p.Age18Told = true
		say = append(say,
			"Como vende licor, dejé activado el <b>control de mayores de 18 años</b> para su catálogo público. Eso lo exige la ley y ya queda cuidado. ✅")
	}
	fu := nextFollowUp(p)
	if fu == nil {
		return proposeTurn(p, say)
	}
	p.Asked = append(p.Asked, fu.Key)
	say = append(say, fu.Question)
	return AgentTurn{Phase: AgentPhaseFollowUps, Profile: p, Say: say,
		PendingKey: fu.Key, Chips: followUpChips(*fu)}
}

func followUpChips(fu followUpDef) []AgentChip {
	yes, no := fu.YesLabel, fu.NoLabel
	if yes == "" {
		yes = "Sí"
	}
	if no == "" {
		no = "No"
	}
	return []AgentChip{{ID: ChipYes, Label: yes}, {ID: ChipNo, Label: no}}
}

// pendingFollowUp: the question currently awaiting an answer (last asked).
func pendingFollowUp(p models.AgentProfile) *followUpDef {
	if len(p.Asked) == 0 {
		return nil
	}
	last := p.Asked[len(p.Asked)-1]
	if _, answered := p.Attrs[last]; answered {
		return nil
	}
	for i := range agentFollowUps {
		if agentFollowUps[i].Key == last {
			return &agentFollowUps[i]
		}
	}
	return nil
}

func advanceFollowUps(p models.AgentProfile, in AgentTurnInput) AgentTurn {
	fu := pendingFollowUp(p)
	if fu == nil {
		// Nothing pending (e.g. resumed session) → ask next or propose.
		return continueFollowUps(p, nil)
	}

	var val *bool
	switch {
	case in.ChipID == ChipYes:
		b := true
		val = &b
	case in.ChipID == ChipNo:
		b := false
		val = &b
	case in.YesNoAnswer != nil:
		switch *in.YesNoAnswer {
		case "yes":
			b := true
			val = &b
		case "no":
			b := false
			val = &b
		default: // unclear
			if !p.UnclearRetry {
				// Single re-ask of the same question (spec §9).
				p.UnclearRetry = true
				return AgentTurn{Phase: AgentPhaseFollowUps, Profile: p,
					Say:        []string{"¿Me responde sí o no, por favor? 🙂", fu.Question},
					PendingKey: fu.Key, Chips: followUpChips(*fu)}
			}
			// Second unclear → conservative default (No) and move on.
			b := false
			val = &b
		}
	case strings.TrimSpace(in.Text) != "":
		if yn := ParseYesNo(in.Text); yn != nil {
			val = yn
		} else {
			return AgentTurn{Phase: AgentPhaseFollowUps, Profile: p, NeedsModel: NeedsModelYesNo, PendingKey: fu.Key}
		}
	default:
		return AgentTurn{Phase: AgentPhaseFollowUps, Profile: p,
			Say: []string{fu.Question}, PendingKey: fu.Key, Chips: followUpChips(*fu)}
	}

	p.Attrs[fu.Key] = *val
	p.UnclearRetry = false
	return continueFollowUps(p, nil)
}

func continueFollowUps(p models.AgentProfile, say []string) AgentTurn {
	fu := nextFollowUp(p)
	if fu == nil {
		return proposeTurn(p, say)
	}
	p.Asked = append(p.Asked, fu.Key)
	return AgentTurn{Phase: AgentPhaseFollowUps, Profile: p,
		Say: append(say, fu.Question), PendingKey: fu.Key, Chips: followUpChips(*fu)}
}

func proposeTurn(p models.AgentProfile, say []string) AgentTurn {
	p.Adjusting = false
	prop := BuildAgentProposal(p)
	name := ""
	if p.BusinessName != "" {
		name = ", <b>" + p.BusinessName + "</b>"
	}
	say = append(say,
		fmt.Sprintf("¡Listo%s! 🎉 Así quedaría su tienda. Lo demás se lo iré mostrando poco a poco, sin llenarle la pantalla.", name),
		"¿Creamos su tienda con esta configuración?")
	return AgentTurn{Phase: AgentPhasePropose, Profile: p, Say: say, Proposal: &prop,
		Chips: []AgentChip{
			{ID: ChipConfirm, Label: "Sí, crear mi tienda 🚀"},
			{ID: ChipAdjust, Label: "Quiero ajustar algo"},
		}}
}

func advancePropose(p models.AgentProfile, in AgentTurnInput) AgentTurn {
	if in.ChipID == ChipAdjust && !p.Adjusting {
		p.Adjusting = true
		return AgentTurn{Phase: AgentPhasePropose, Profile: p, Say: []string{
			"Claro. Dígame qué quiere cambiar: por ejemplo <i>«quite el fiado»</i> o <i>«agregue mesas»</i>.",
		}}
	}
	if p.Adjusting && strings.TrimSpace(in.Text) != "" {
		p = applyAdjustment(p, in.Text)
		return proposeTurn(p, []string{"Hecho ✅ Actualicé la propuesta."})
	}
	if in.ChipID == ChipConfirm || boolFromYesNo(in.Text) {
		// The actual profile application happens in the confirm endpoint —
		// the machine just reports the terminal state.
		return AgentTurn{Phase: AgentPhaseDone, Profile: p, Done: true, Say: []string{
			"¡Su tienda quedó lista! 🎉 Cuando quiera cambiar algo, me dice — estaré aquí en su panel. 💙",
		}}
	}
	return proposeTurn(p, nil)
}

func boolFromYesNo(text string) bool {
	yn := ParseYesNo(text)
	return yn != nil && *yn
}

// applyAdjustment interprets simple natural-language tweaks over the proposal
// (FR-07). Deterministic keyword matching: negation words flip to OFF.
func applyAdjustment(p models.AgentProfile, text string) models.AgentProfile {
	t := strings.ToLower(text)
	negated := false
	for _, n := range []string{"quite", "quitar", "sin ", "no quiero", "elimine", "borre", "no "} {
		if strings.Contains(t, n) {
			negated = true
			break
		}
	}
	keywords := map[string]string{
		"fiado": "fiado", "fiar": "fiado",
		"mesa": "mesas", "mesas": "mesas",
		"domicilio": "domicilios",
		"granel":    "granel", "bulto": "granel",
		"equipo": "equipo", "comision": "equipo", "comisión": "equipo",
	}
	for kw, attr := range keywords {
		if strings.Contains(t, kw) {
			p.Attrs[attr] = !negated
		}
	}
	return p
}

// BuildAgentProposal derives the module summary from the profile. Labels are
// user-facing Spanish; the real grid is resolved by the F041 catalog once the
// profile is applied — this list is the conversational preview (FR-06).
func BuildAgentProposal(p models.AgentProfile) AgentProposal {
	grid := []string{"Vender", "Productos", "Historial", "Ganancias"}
	types := typeSet(p)

	if isFood(types) {
		grid = append(grid, "Menú y recetas")
	}
	if p.Attrs["mesas"] {
		grid = append(grid, "Mesas")
	}
	if p.Attrs["domicilios"] {
		grid = append(grid, "Domicilios")
	}
	if p.Attrs["fiado"] {
		grid = append(grid, "Cuaderno de fiados")
	}
	if types[models.BusinessTypePeluqueria] {
		grid = append(grid, "Servicios")
	}
	if p.Attrs["equipo"] {
		grid = append(grid, "Comisiones")
	}
	if p.Attrs["granel"] || types[models.BusinessTypeDepositoConstruccion] {
		grid = append(grid, "Granel y bultos")
	}
	if types[models.BusinessTypeAcademias] {
		grid = append(grid, "Eventos")
	}
	if anyOf(types, models.BusinessTypeProveedorAgricola, models.BusinessTypeProveedorMayorista) {
		grid = append(grid, "Panel de proveedor")
	}
	if p.Age18 {
		grid = append(grid, "Catálogo 18+")
	}

	return AgentProposal{
		Grid: grid,
		Reel: []string{"Catálogo online", "Vender por voz", "Campañas WhatsApp"},
	}
}
