// Spec: specs/107-dashboard-v2-resumen/spec.md
//
// Turnos del modo ASISTENTE de Vendi (kind "assist", botón central del
// Dashboard v2). Mismo endpoint /onboarding/agent/turn: el despacho por
// kind vive en OnboardingAgentTurn. Gate de acciones: la propuesta queda
// guardada en la sesión y SOLO el chip confirm_action la ejecuta (FR-08b).
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
)

const (
	chipConfirmAction = "confirm_action"
	chipCancelAction  = "cancel_action"
)

func assistGreeting(ownerName string) []string {
	hola := "¡Hola! 👋 Soy <b>Vendi</b>."
	if n := services.AgentFirstName(ownerName); n != "" {
		hola = fmt.Sprintf("¡Hola, %s! 👋 Soy <b>Vendi</b>.", n)
	}
	return []string{
		hola + " Pregúnteme lo que quiera de su negocio — ventas, fiados, inventario — o pídame crear un producto o un cliente.",
	}
}

// tenantOwnerName: nombre del dueño para dirigirse a él (vacío si no carga).
func tenantOwnerName(db *gorm.DB, tenantID string) string {
	var t models.Tenant
	if err := db.Select("owner_name").First(&t, "id = ?", tenantID).Error; err != nil {
		return ""
	}
	return t.OwnerName
}

func handleAssistTurn(c *gin.Context, db *gorm.DB, ai AgentAI,
	session models.AgentSession, req agentTurnRequest, created bool) {

	tenantID := session.TenantID

	// Primer contacto o reanudación sin input → saludo (no consume turno IA).
	if created || (req.Text == "" && req.Chip == "" && session.Turns == 0) {
		turn := services.AgentTurn{Phase: "assist", Profile: session.Profile,
			Say: assistGreeting(tenantOwnerName(db, tenantID))}
		if err := persistTurn(db, &session, agentTurnRequest{}, turn, nil, 0); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la conversación"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": turnResponse(session, turn)})
		return
	}
	if req.Text == "" && req.Chip == "" {
		turn := services.AgentTurn{Phase: "assist", Profile: session.Profile,
			Say: []string{"Sigo aquí. ¿En qué le ayudo?"}}
		c.JSON(http.StatusOK, gin.H{"data": turnResponse(session, turn)})
		return
	}

	// ── Gate de acciones: confirmar / cancelar la propuesta pendiente ──
	if req.Chip == chipConfirmAction || req.Chip == chipCancelAction {
		pending := session.Profile.PendingAction
		profile := session.Profile
		profile.PendingAction = nil

		var say []string
		var result *services.AssistActionResult
		if req.Chip == chipCancelAction || pending == nil {
			say = []string{"Listo, no hago nada. ¿Algo más?"}
		} else {
			branchID := middleware.GetBranchID(c)
			userID := middleware.GetUserID(c)
			r := services.ExecuteAssistAction(db, tenantID, branchID, userID,
				&services.AgentAssistAction{Type: pending.Type, Params: pending.Params})
			result = &r
			say = []string{r.Say}
		}

		turn := services.AgentTurn{Phase: "assist", Profile: profile, Say: say}
		if err := persistTurn(db, &session, req, turn,
			map[string]any{"action_executed": req.Chip == chipConfirmAction}, 0); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la conversación"})
			return
		}
		resp := turnResponse(session, turn)
		resp.ActionResult = result
		c.JSON(http.StatusOK, gin.H{"data": resp})
		return
	}

	// ── Texto libre → interpretación (presupuesto igual que onboarding) ──
	if session.ModelCalls >= services.MaxAgentModelCalls {
		c.JSON(http.StatusOK, gin.H{"data": degradedTurn(session, "budget", false)})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), agentAITimeout)
	defer cancel()

	assistCtx := services.BuildAssistContext(db, tenantID, startOfTenantDay(tenantNow()))
	start := time.Now()
	say, action, err := ai.InterpretAssist(ctx, assistCtx, req.Text)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"data": degradedTurn(session, "ai_unavailable", false)})
		return
	}
	session.ModelCalls++

	profile := session.Profile
	profile.PendingAction = nil
	turn := services.AgentTurn{Phase: "assist", Profile: profile}
	if say != "" {
		turn.Say = append(turn.Say, say)
	}
	extraction := map[string]any{"say": say}
	if action != nil {
		profile.PendingAction = &models.AgentPendingAction{Type: action.Type, Params: action.Params}
		turn.Profile = profile
		if summary := services.AssistActionSummary(action); summary != "" {
			turn.Say = append(turn.Say, summary)
		}
		turn.Chips = []services.AgentChip{
			{ID: chipConfirmAction, Label: "Sí, hágalo"},
			{ID: chipCancelAction, Label: "No, gracias"},
		}
		extraction["action"] = action
	}
	if len(turn.Say) == 0 {
		turn.Say = []string{"¿Me lo repite con otras palabras, por favor? 🙂"}
	}

	if err := persistTurn(db, &session, req, turn, extraction,
		int(time.Since(start).Milliseconds())); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la conversación"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": turnResponse(session, turn)})
}
