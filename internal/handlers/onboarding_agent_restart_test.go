// Spec: specs/106-onboarding-conversacional-agente/spec.md (Adenda A.3)
package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/models"
)

// AC-A8: restart abandona la sesión activa y crea una nueva.
func TestAgentTurnRestartAbandonsAndCreatesNew(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	r := agentRouter(db, &fakeAgentAI{}, tenant.ID)

	code, data := agentPost(t, r, "/turn", map[string]any{})
	require.Equal(t, http.StatusOK, code)
	firstID, _ := data["session_id"].(string)
	require.NotEmpty(t, firstID)

	code, data = agentPost(t, r, "/turn", map[string]any{"text": "La Esquina"})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "ask_description", data["phase"])

	// Restart: nueva sesión desde el saludo, la anterior queda abandoned.
	code, data = agentPost(t, r, "/turn", map[string]any{"restart": true})
	require.Equal(t, http.StatusOK, code)
	secondID, _ := data["session_id"].(string)
	require.NotEmpty(t, secondID)
	assert.NotEqual(t, firstID, secondID)
	assert.Equal(t, "ask_name", data["phase"])

	var old models.AgentSession
	require.NoError(t, db.First(&old, "id = ?", firstID).Error)
	assert.Equal(t, models.AgentSessionStatusAbandoned, old.Status)

	// Sin restart, la nueva sesión se reanuda normal.
	code, data = agentPost(t, r, "/turn", map[string]any{})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, secondID, data["session_id"])
}

// Adenda A: el saludo usa el nombre del dueño que el registro ya conoce.
func TestAgentGreetingsUseOwnerFirstName(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	require.NoError(t, db.Model(&models.Tenant{}).Where("id = ?", tenant.ID).
		Update("owner_name", "carmen lópez").Error)
	r := agentRouter(db, &fakeAgentAI{}, tenant.ID)

	_, data := agentPost(t, r, "/turn", map[string]any{})
	say := data["say"].([]any)[0].(string)
	assert.Contains(t, say, "Carmen")

	_, data = agentPost(t, r, "/turn", map[string]any{"kind": "assist"})
	say = data["say"].([]any)[0].(string)
	assert.Contains(t, say, "Carmen")
}
