// Spec: specs/106-onboarding-conversacional-agente/spec.md
//
// HTTP endpoints for the Vendi conversational onboarding:
//
//	POST /api/v1/onboarding/agent/turn     — advance the conversation
//	POST /api/v1/onboarding/agent/confirm  — apply the proposed profile
//	POST /api/v1/onboarding/agent/fallback — no-AI manual completion
//
// All authenticated (tenant from JWT). AI failures NEVER surface as 5xx: the
// response degrades (degraded=true + reason) and the UI offers the fallback
// (Art. I/II, AC-10). Every turn is persisted as training corpus (FR-08).
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
)

// AgentAI is the interpretation seam — *services.GeminiService implements it;
// tests script it. Methods must be nil-receiver-safe (return error).
type AgentAI interface {
	InterpretAgentDescription(ctx context.Context, text string) (*services.AgentExtraction, error)
	InterpretAgentYesNo(ctx context.Context, question, text string) (string, error)
	// Spec 107 — modo asistente del botón central del Dashboard v2.
	InterpretAssist(ctx context.Context, contextJSON, text string) (string, *services.AgentAssistAction, error)
}

const agentAITimeout = 45 * time.Second

// ── wire types ──────────────────────────────────────────────────────────────

type agentTurnRequest struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	Chip      string `json:"chip"`
	// Kind: "onboarding" (default) | "assist" (Spec 107, botón central).
	Kind string `json:"kind"`
	// Restart (Adenda A.3): abandona la sesión activa y arranca una nueva —
	// el "Empezar de nuevo" del chat. La abandonada queda para el corpus.
	Restart bool `json:"restart"`
}

type agentTypeWire struct {
	Key        string  `json:"key"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	Primary    bool    `json:"primary"`
}

type agentProfileWire struct {
	BusinessName string          `json:"business_name"`
	Types        []agentTypeWire `json:"types"`
	Attrs        map[string]bool `json:"attrs"`
	Age18        bool            `json:"age18"`
	Corrected    bool            `json:"corrected"`
}

type agentTurnResponse struct {
	SessionID     string                  `json:"session_id"`
	Phase         string                  `json:"phase"`
	Say           []string                `json:"say"`
	Chips         []services.AgentChip    `json:"chips"`
	Profile       agentProfileWire        `json:"profile"`
	Proposal      *services.AgentProposal `json:"proposal,omitempty"`
	Done          bool                    `json:"done"`
	OfferFallback bool                    `json:"offer_fallback"`
	// PendingKey: follow-up en curso — el frontend lo usa para matizar el
	// gesto/forma del orbe (Adenda A). Aditivo y retrocompatible.
	PendingKey string `json:"pending_key,omitempty"`
	Degraded      bool                    `json:"degraded"`
	Reason        string                  `json:"reason,omitempty"`
	// Spec 107 — resultado de una acción assist ejecutada.
	ActionResult *services.AssistActionResult `json:"action_result,omitempty"`
}

func profileWire(p models.AgentProfile) agentProfileWire {
	types := make([]agentTypeWire, 0, len(p.Types))
	for i, tg := range p.Types {
		types = append(types, agentTypeWire{
			Key: tg.Key, Label: services.AgentTypeLabel(tg.Key),
			Confidence: tg.Confidence, Primary: i == 0,
		})
	}
	attrs := p.Attrs
	if attrs == nil {
		attrs = map[string]bool{}
	}
	return agentProfileWire{
		BusinessName: p.BusinessName, Types: types, Attrs: attrs,
		Age18: p.Age18, Corrected: p.Corrected,
	}
}

// ── session helpers ─────────────────────────────────────────────────────────

// loadOrCreateSession returns the tenant's single active onboarding session,
// creating it (with Vendi's greeting as event #1) on first contact.
func loadOrCreateSession(db *gorm.DB, tenantID, model, kind string) (models.AgentSession, bool, error) {
	if kind == "" {
		kind = models.AgentSessionKindOnboarding
	}
	promptVersion := services.OnboardingAgentPromptVersion
	phase := services.AgentPhaseAskName
	if kind == services.AgentSessionKindAssist {
		promptVersion = services.AssistPromptVersion
		phase = "assist"
	}
	var session models.AgentSession
	err := db.Where("tenant_id = ? AND kind = ? AND status = ?",
		tenantID, kind, models.AgentSessionStatusActive).
		Order("created_at DESC").First(&session).Error
	if err == nil {
		return session, false, nil
	}
	if err != gorm.ErrRecordNotFound {
		return session, false, err
	}

	session = models.AgentSession{
		TenantID:      tenantID,
		Kind:          kind,
		Channel:       "app",
		Model:         model,
		PromptVersion: promptVersion,
		Status:        models.AgentSessionStatusActive,
		Phase:         phase,
		Profile:       models.AgentProfile{},
	}
	if err := db.Create(&session).Error; err != nil {
		return session, false, err
	}
	return session, true, nil
}

// persistTurn writes the user + assistant events and the session snapshot in
// one transaction. The (session_id, seq) unique index turns a double-tap race
// into a conflict → the caller returns the current state instead of duplicating.
func persistTurn(db *gorm.DB, session *models.AgentSession, req agentTurnRequest,
	turn services.AgentTurn, extraction map[string]any, latencyMs int) error {

	return db.Transaction(func(tx *gorm.DB) error {
		baseSeq := session.Turns * 2
		if req.Text != "" || req.Chip != "" {
			userEvent := models.AgentSessionEvent{
				SessionID:   session.ID,
				TenantID:    session.TenantID,
				Seq:         baseSeq + 1,
				Role:        models.AgentEventRoleUser,
				DisplayText: strings.TrimSpace(req.Text + " " + req.Chip),
				RawText:     req.Text,
				Extraction:  orEmpty(extraction),
				LatencyMs:   latencyMs,
			}
			if err := tx.Create(&userEvent).Error; err != nil {
				return err
			}
		}
		assistantEvent := models.AgentSessionEvent{
			SessionID:   session.ID,
			TenantID:    session.TenantID,
			Seq:         baseSeq + 2,
			Role:        models.AgentEventRoleAssistant,
			DisplayText: strings.Join(turn.Say, "\n"),
			Extraction:  map[string]any{},
		}
		if err := tx.Create(&assistantEvent).Error; err != nil {
			return err
		}

		session.Turns++
		session.Phase = turn.Phase
		session.Profile = turn.Profile
		return tx.Model(&models.AgentSession{}).Where("id = ?", session.ID).Updates(map[string]any{
			"turns":       session.Turns,
			"phase":       session.Phase,
			"profile":     jsonString(turn.Profile),
			"model_calls": session.ModelCalls,
		}).Error
	})
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func jsonString(v any) string {
	// GORM's map-based Updates bypasses the struct serializer, so the JSONB
	// column gets the JSON text explicitly (same technique as feature_flags).
	b, _ := json.Marshal(v)
	return string(b)
}

func degradedTurn(session models.AgentSession, reason string, offerFallback bool) agentTurnResponse {
	return agentTurnResponse{
		SessionID: session.ID,
		Phase:     session.Phase,
		Say: []string{
			"En este momento no puedo pensar con claridad 😅 Puede intentar de nuevo, o escoger usted mismo los tipos de su negocio y seguimos.",
		},
		Profile:       profileWire(session.Profile),
		Degraded:      true,
		Reason:        reason,
		OfferFallback: offerFallback,
	}
}

// ── handlers ────────────────────────────────────────────────────────────────

// OnboardingAgentTurn — POST /api/v1/onboarding/agent/turn
func OnboardingAgentTurn(db *gorm.DB, ai AgentAI) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req agentTurnRequest
		if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida"})
			return
		}
		req.Text = strings.TrimSpace(req.Text)

		if req.Restart {
			kind := req.Kind
			if kind == "" {
				kind = models.AgentSessionKindOnboarding
			}
			if err := db.Model(&models.AgentSession{}).
				Where("tenant_id = ? AND kind = ? AND status = ?",
					tenantID, kind, models.AgentSessionStatusActive).
				Update("status", models.AgentSessionStatusAbandoned).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo reiniciar la conversación"})
				return
			}
		}

		session, created, err := loadOrCreateSession(db, tenantID, agentModelName(ai), req.Kind)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo cargar la conversación"})
			return
		}

		// Spec 107 — el modo asistente tiene su propio flujo (sin máquina
		// de fases del onboarding).
		if session.Kind == services.AgentSessionKindAssist {
			handleAssistTurn(c, db, ai, session, req, created)
			return
		}

		// First contact: greet without consuming input.
		if created || (req.Text == "" && req.Chip == "" && session.Turns == 0) {
			turn := services.AgentTurn{Phase: session.Phase, Profile: session.Profile, Say: services.AgentGreeting()}
			if err := persistTurn(db, &session, agentTurnRequest{}, turn, nil, 0); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la conversación"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"data": turnResponse(session, turn)})
			return
		}

		// Resume without input: re-emit the current question, don't persist.
		if req.Text == "" && req.Chip == "" {
			turn := services.AdvanceAgent(session.Phase, session.Profile, services.AgentTurnInput{})
			if turn.NeedsModel != "" {
				turn = services.AgentTurn{Phase: session.Phase, Profile: session.Profile,
					Say: []string{"Sigamos donde íbamos. 🙂"}}
			}
			c.JSON(http.StatusOK, gin.H{"data": turnResponse(session, turn)})
			return
		}

		input := services.AgentTurnInput{Text: req.Text, ChipID: req.Chip}
		turn := services.AdvanceAgent(session.Phase, session.Profile, input)

		var extractionBlob map[string]any
		latencyStart := time.Now()

		if turn.NeedsModel != "" {
			if session.ModelCalls >= services.MaxAgentModelCalls {
				c.JSON(http.StatusOK, gin.H{"data": degradedTurn(session, "budget", true)})
				return
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), agentAITimeout)
			defer cancel()

			switch turn.NeedsModel {
			case services.NeedsModelDescription:
				ext, aiErr := ai.InterpretAgentDescription(ctx, req.Text)
				if aiErr != nil {
					c.JSON(http.StatusOK, gin.H{"data": degradedTurn(session, "ai_unavailable", true)})
					return
				}
				services.SanitizeAgentExtraction(ext)
				input.Extraction = ext
				extractionBlob = map[string]any{"types": ext.Types, "attrs": ext.Attrs}
			case services.NeedsModelYesNo:
				question := services.AgentFollowUpQuestion(turn.PendingKey)
				answer, aiErr := ai.InterpretAgentYesNo(ctx, question, req.Text)
				if aiErr != nil {
					c.JSON(http.StatusOK, gin.H{"data": degradedTurn(session, "ai_unavailable", true)})
					return
				}
				input.YesNoAnswer = &answer
				extractionBlob = map[string]any{"yes_no": answer, "question_key": turn.PendingKey}
			}
			session.ModelCalls++
			turn = services.AdvanceAgent(session.Phase, session.Profile, input)
		}

		latencyMs := int(time.Since(latencyStart).Milliseconds())
		if err := persistTurn(db, &session, req, turn, extractionBlob, latencyMs); err != nil {
			// Unique (session_id, seq) conflict = concurrent double-tap: the
			// other request already advanced. Return the stored state.
			var fresh models.AgentSession
			if lerr := db.Where("id = ?", session.ID).First(&fresh).Error; lerr == nil {
				resume := services.AdvanceAgent(fresh.Phase, fresh.Profile, services.AgentTurnInput{})
				if resume.NeedsModel != "" {
					resume = services.AgentTurn{Phase: fresh.Phase, Profile: fresh.Profile}
				}
				c.JSON(http.StatusOK, gin.H{"data": turnResponse(fresh, resume)})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la conversación"})
			return
		}

		// Terminal confirm arriving through /turn (defensive): apply profile
		// so the session never lands "done" without the tenant configured.
		if turn.Done && session.Status == models.AgentSessionStatusActive {
			if err := applyAgentSession(db, &session, services.AgentSessionFinalStatus(session.Profile)); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo aplicar la configuración"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": turnResponse(session, turn)})
	}
}

func turnResponse(session models.AgentSession, turn services.AgentTurn) agentTurnResponse {
	return agentTurnResponse{
		SessionID:     session.ID,
		Phase:         turn.Phase,
		Say:           turn.Say,
		Chips:         turn.Chips,
		Profile:       profileWire(turn.Profile),
		Proposal:      turn.Proposal,
		Done:          turn.Done,
		OfferFallback: turn.OfferFallback,
		PendingKey:    turn.PendingKey,
	}
}

// agentModelName pins the model identity onto the session for the corpus.
func agentModelName(ai AgentAI) string {
	if svc, ok := ai.(*services.GeminiService); ok && svc != nil {
		return svc.ModelName()
	}
	return ""
}

// applyAgentSession materializes the profile and closes the session.
func applyAgentSession(db *gorm.DB, session *models.AgentSession, status string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var tenant models.Tenant
		if err := tx.Where("id = ?", session.TenantID).First(&tenant).Error; err != nil {
			return err
		}
		updates, err := services.BuildAgentTenantUpdates(tenant, session.Profile)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.Tenant{}).Where("id = ?", tenant.ID).Updates(updates).Error; err != nil {
			return err
		}
		session.Status = status
		session.Phase = services.AgentPhaseDone
		return tx.Model(&models.AgentSession{}).Where("id = ?", session.ID).Updates(map[string]any{
			"status": status,
			"phase":  services.AgentPhaseDone,
		}).Error
	})
}

// OnboardingAgentConfirm — POST /api/v1/onboarding/agent/confirm
func OnboardingAgentConfirm(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req struct {
			SessionID string `json:"session_id" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "falta el identificador de la conversación"})
			return
		}

		var session models.AgentSession
		if err := db.Where("id = ? AND tenant_id = ?", req.SessionID, tenantID).
			First(&session).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "conversación no encontrada"})
			return
		}
		if session.Status != models.AgentSessionStatusActive {
			c.JSON(http.StatusOK, gin.H{"data": gin.H{
				"onboarding_completed": true,
				"profile":              profileWire(session.Profile),
			}})
			return
		}

		if err := applyAgentSession(db, &session, services.AgentSessionFinalStatus(session.Profile)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo aplicar la configuración"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"onboarding_completed": true,
			"profile":              profileWire(session.Profile),
		}})
	}
}

// OnboardingAgentFallback — POST /api/v1/onboarding/agent/fallback
// The no-AI path (FR-10): the tendero picked types (multi-select) and basic
// toggles by hand; the same apply pipeline configures the tenant and the
// session is recorded with outcome `fallback` for the corpus.
func OnboardingAgentFallback(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req struct {
			SessionID    string          `json:"session_id"`
			BusinessName string          `json:"business_name"`
			Types        []string        `json:"types" binding:"required,min=1"`
			Attrs        map[string]bool `json:"attrs"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "seleccione al menos un tipo de negocio"})
			return
		}

		profile := models.AgentProfile{BusinessName: strings.TrimSpace(req.BusinessName)}
		for _, t := range req.Types {
			key := strings.TrimSpace(t)
			if _, ok := models.ValidBusinessTypes[key]; !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "tipo de negocio no válido"})
				return
			}
			profile.Types = append(profile.Types, models.AgentTypeGuess{Key: key, Confidence: 1})
			if key == models.BusinessTypeBar {
				profile.Age18 = true
			}
		}
		profile.Attrs = map[string]bool{}
		for k, v := range req.Attrs {
			switch k {
			case "mesas", "domicilios", "fiado", "equipo", "granel":
				profile.Attrs[k] = v
			}
		}

		session, _, err := loadOrCreateSession(db, tenantID, "", models.AgentSessionKindOnboarding)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo cargar la conversación"})
			return
		}
		session.Profile = profile
		if err := db.Model(&models.AgentSession{}).Where("id = ?", session.ID).
			Update("profile", jsonString(profile)).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la conversación"})
			return
		}

		if err := applyAgentSession(db, &session, models.AgentSessionStatusFallback); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo aplicar la configuración"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"onboarding_completed": true,
			"profile":              profileWire(profile),
		}})
	}
}
