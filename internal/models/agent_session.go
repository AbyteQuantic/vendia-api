// Spec: specs/106-onboarding-conversacional-agente/spec.md
package models

// AgentSession is one conversation between a tenant and the VendIA assistant
// ("Vendi"). Kind is extensible: v1 only ships "onboarding", but the same
// corpus feeds the future in-house agent for other tenant-assist tasks.
//
// Every session is training data: it stores the model + prompt version used,
// the working/final profile, the outcome, and cost metrics. Raw turns live in
// AgentSessionEvent. Retention is indefinite (spec §5 RNF); anonymization
// happens before any training use, never at write time.
const (
	AgentSessionKindOnboarding = "onboarding"

	AgentSessionStatusActive = "active"
	// Confirmed: tendero accepted the first interpretation.
	AgentSessionStatusConfirmed = "confirmed"
	// Corrected: confirmed, but only after >=1 correction of the
	// interpretation — the highest-value training examples (FR-09).
	AgentSessionStatusCorrected = "corrected"
	AgentSessionStatusAbandoned = "abandoned"
	// Fallback: finished through the no-AI minimal type-selection screen.
	AgentSessionStatusFallback = "fallback"

	AgentEventRoleAssistant = "assistant"
	AgentEventRoleUser      = "user"
	AgentEventRoleSystem    = "system"
)

// AgentTypeGuess is one detected business type with the model's confidence.
// The slice in AgentProfile is ordered: position 0 = primary type (FR-14).
type AgentTypeGuess struct {
	Key        string  `json:"key"`
	Confidence float64 `json:"confidence"`
}

// AgentProfile is the working (and, once confirmed, final) business profile a
// session builds up. Stored as JSONB on the session so an abandoned
// conversation resumes exactly where it stopped (FR-11 / AC-11).
type AgentProfile struct {
	BusinessName string           `json:"business_name,omitempty"`
	Types        []AgentTypeGuess `json:"types,omitempty"`
	// Attrs holds answered operational attributes by canonical key:
	// mesas, domicilios, fiado, equipo, granel. Absent key = not asked yet.
	Attrs map[string]bool `json:"attrs,omitempty"`
	// Asked tracks follow-up keys already asked (dedup + turn cap).
	Asked []string `json:"asked,omitempty"`
	// Age18: platform rule communicated (never asked) when alcohol is
	// involved — the per-product 18+ gates (Specs 063/103) stay fail-closed
	// regardless; this records that Vendi told the tendero (FR-05).
	Age18     bool `json:"age18,omitempty"`
	Age18Told bool `json:"age18_told,omitempty"`
	// Corrected: the tendero rejected an interpretation at least once (FR-09).
	Corrected bool `json:"corrected,omitempty"`
	// DescriptionAttempts counts descriptions that yielded zero types; after
	// 2 the assistant offers the manual fallback (spec §9).
	DescriptionAttempts int `json:"description_attempts,omitempty"`
	// UnclearRetry: the single re-ask allowed for an ambiguous closed answer.
	UnclearRetry bool `json:"unclear_retry,omitempty"`
	// Adjusting: tendero asked to tweak the proposal before confirming (FR-07).
	Adjusting bool `json:"adjusting,omitempty"`
}

type AgentSession struct {
	BaseModel

	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Kind     string `gorm:"type:varchar(32);not null;default:'onboarding'" json:"kind"`
	Channel  string `gorm:"type:varchar(16);not null;default:'app'" json:"channel"`
	// Model + PromptVersion pin how each session was produced so the corpus
	// can be sliced when training the in-house agent (FR-08).
	Model         string `gorm:"type:varchar(64);not null;default:''" json:"model"`
	PromptVersion string `gorm:"type:varchar(32);not null;default:''" json:"prompt_version"`

	Status string `gorm:"type:varchar(16);not null;default:'active';index" json:"status"`
	// Phase is the state-machine position (services.AgentPhase*) so an
	// interrupted conversation resumes at the last answered turn.
	Phase string `gorm:"type:varchar(32);not null;default:'ask_name'" json:"phase"`

	Profile AgentProfile `gorm:"serializer:json;type:jsonb;not null;default:'{}'" json:"profile"`

	// ModelCalls enforces the hard per-session AI budget (AC-14).
	ModelCalls int   `gorm:"not null;default:0" json:"model_calls"`
	Turns      int   `gorm:"not null;default:0" json:"turns"`
	DurationMs int64 `gorm:"not null;default:0" json:"duration_ms"`
}

// AgentSessionEvent is one turn of a session. RawText keeps the tendero's
// words verbatim — without the raw input the corpus is useless for training
// (spec §7). The (session_id, seq) unique index makes a double-tapped send
// idempotent: the second insert conflicts instead of duplicating the turn.
type AgentSessionEvent struct {
	BaseModel

	SessionID string `gorm:"type:uuid;not null;uniqueIndex:idx_agent_event_session_seq,priority:1" json:"session_id"`
	TenantID  string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Seq       int    `gorm:"not null;uniqueIndex:idx_agent_event_session_seq,priority:2" json:"seq"`
	// Role: assistant | user | system (AgentEventRole*).
	Role string `gorm:"type:varchar(12);not null" json:"role"`
	// DisplayText: what the UI showed (assistant bubbles / chip label).
	DisplayText string `gorm:"type:text;not null;default:''" json:"display_text"`
	// RawText: the tendero's verbatim input (user events only).
	RawText string `gorm:"type:text;not null;default:''" json:"raw_text"`
	// Extraction: the model's structured output for this turn, if any.
	Extraction map[string]any `gorm:"serializer:json;type:jsonb;not null;default:'{}'" json:"extraction"`
	LatencyMs  int            `gorm:"not null;default:0" json:"latency_ms"`
}
