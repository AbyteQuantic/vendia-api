// Spec: specs/106-onboarding-conversacional-agente/spec.md
package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
)

// fakeAgentAI scripts the interpreter so handler tests never touch Gemini.
type fakeAgentAI struct {
	extraction *services.AgentExtraction
	yesNo      string
	err        error
	calls      int
}

func (f *fakeAgentAI) InterpretAgentDescription(_ context.Context, _ string) (*services.AgentExtraction, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.extraction, nil
}

func (f *fakeAgentAI) InterpretAgentYesNo(_ context.Context, _, _ string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.yesNo, nil
}

// agentRouter mounts the three endpoints behind a stub auth middleware that
// injects the given tenant id (same contract middleware.Auth provides).
func agentRouter(db *gorm.DB, ai handlers.AgentAI, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/turn", handlers.OnboardingAgentTurn(db, ai))
	r.POST("/confirm", handlers.OnboardingAgentConfirm(db))
	r.POST("/fallback", handlers.OnboardingAgentFallback(db))
	return r
}

func agentPost(t *testing.T, r *gin.Engine, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out.Data
}

func createAgentTestTenant(t *testing.T, db *gorm.DB) models.Tenant {
	t.Helper()
	tenant := models.Tenant{
		OwnerName:    "Test",
		Phone:        uniquePhone(),
		PasswordHash: "x",
		BusinessName: "Mi negocio",
		SaleTypes:    []string{"products"},
	}
	require.NoError(t, db.Create(&tenant).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.AgentSessionEvent{})
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.AgentSession{})
		db.Unscoped().Delete(&tenant)
	})
	return tenant
}

func tiendaLicoresExtraction() *services.AgentExtraction {
	tr := true
	return &services.AgentExtraction{
		Types: []models.AgentTypeGuess{
			{Key: models.BusinessTypeTiendaBarrio, Confidence: 0.95},
			{Key: models.BusinessTypeBar, Confidence: 0.9},
		},
		Attrs: map[string]*bool{"licores": &tr},
	}
}

func TestAgentTurnCreatesAndResumesSession(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	r := agentRouter(db, &fakeAgentAI{}, tenant.ID)

	// First contact: greeting, session created (AC-01 backend side).
	code, data := agentPost(t, r, "/turn", map[string]any{})
	require.Equal(t, http.StatusOK, code)
	sessionID, _ := data["session_id"].(string)
	require.NotEmpty(t, sessionID)
	assert.Equal(t, "ask_name", data["phase"])
	assert.NotEmpty(t, data["say"])

	// Name answered → description phase.
	code, data = agentPost(t, r, "/turn", map[string]any{"text": "La Esquina"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ask_description", data["phase"])

	// A new request WITHOUT session_id resumes the same active session (AC-11).
	code, data = agentPost(t, r, "/turn", map[string]any{})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, sessionID, data["session_id"])
	assert.Equal(t, "ask_description", data["phase"])
}

func TestAgentTurnMultiTypeFlowAndConfirm(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	ai := &fakeAgentAI{extraction: tiendaLicoresExtraction()}
	r := agentRouter(db, ai, tenant.ID)

	_, data := agentPost(t, r, "/turn", map[string]any{})
	sessionID := data["session_id"].(string)
	agentPost(t, r, "/turn", map[string]any{"text": "La Esquina"})

	// AC-02: description → both types detected via the (fake) model.
	code, data := agentPost(t, r, "/turn", map[string]any{"text": "tengo una tienda y vendo cerveza"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "confirm_types", data["phase"])
	profile := data["profile"].(map[string]any)
	types := profile["types"].([]any)
	require.Len(t, types, 2)
	first := types[0].(map[string]any)
	assert.Equal(t, models.BusinessTypeTiendaBarrio, first["key"])
	assert.Equal(t, true, first["primary"])
	assert.Equal(t, 1, ai.calls)

	// Confirm types → 18+ notice + follow-ups (AC-04).
	code, data = agentPost(t, r, "/turn", map[string]any{"chip": "yes"})
	require.Equal(t, http.StatusOK, code)
	profile = data["profile"].(map[string]any)
	assert.Equal(t, true, profile["age18"])

	// Answer follow-ups with chips until the proposal.
	for i := 0; i < 6 && data["phase"] == "follow_ups"; i++ {
		_, data = agentPost(t, r, "/turn", map[string]any{"chip": "no"})
	}
	require.Equal(t, "propose", data["phase"])
	require.NotNil(t, data["proposal"])

	// Confirm endpoint applies the profile (AC-08 input side).
	code, data = agentPost(t, r, "/confirm", map[string]any{"session_id": sessionID})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, data["onboarding_completed"])

	var fresh models.Tenant
	require.NoError(t, db.Where("id = ?", tenant.ID).First(&fresh).Error)
	assert.Equal(t, []string{models.BusinessTypeTiendaBarrio, models.BusinessTypeBar}, fresh.BusinessTypes)
	assert.True(t, fresh.OnboardingCompleted)
	assert.Equal(t, "La Esquina", fresh.BusinessName)

	// AC-09: session + raw turns persisted, model/prompt pinned.
	var session models.AgentSession
	require.NoError(t, db.Where("id = ?", sessionID).First(&session).Error)
	assert.Equal(t, models.AgentSessionStatusConfirmed, session.Status)
	assert.Equal(t, services.OnboardingAgentPromptVersion, session.PromptVersion)
	assert.Equal(t, 1, session.ModelCalls)

	var events []models.AgentSessionEvent
	require.NoError(t, db.Where("session_id = ?", sessionID).Order("seq").Find(&events).Error)
	require.NotEmpty(t, events)
	var rawFound, extractionFound bool
	for _, ev := range events {
		if ev.Role == models.AgentEventRoleUser && ev.RawText == "tengo una tienda y vendo cerveza" {
			rawFound = true
			if len(ev.Extraction) > 0 {
				extractionFound = true
			}
		}
	}
	assert.True(t, rawFound, "raw tendero text must be persisted verbatim")
	assert.True(t, extractionFound, "model extraction must be persisted on the turn")
}

func TestAgentTurnCorrectionMarksSessionCorrected(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	ai := &fakeAgentAI{extraction: tiendaLicoresExtraction()}
	r := agentRouter(db, ai, tenant.ID)

	_, data := agentPost(t, r, "/turn", map[string]any{})
	sessionID := data["session_id"].(string)
	agentPost(t, r, "/turn", map[string]any{"text": "La Esquina"})
	agentPost(t, r, "/turn", map[string]any{"text": "tengo una tienda y vendo cerveza"})
	// Reject the interpretation (AC-06) then re-describe and confirm.
	agentPost(t, r, "/turn", map[string]any{"chip": "no"})
	agentPost(t, r, "/turn", map[string]any{"text": "es una tienda con licores"})
	_, data = agentPost(t, r, "/turn", map[string]any{"chip": "yes"})
	for i := 0; i < 6 && data["phase"] == "follow_ups"; i++ {
		_, data = agentPost(t, r, "/turn", map[string]any{"chip": "no"})
	}
	_, _ = agentPost(t, r, "/confirm", map[string]any{"session_id": sessionID})

	var session models.AgentSession
	require.NoError(t, db.Where("id = ?", sessionID).First(&session).Error)
	assert.Equal(t, models.AgentSessionStatusCorrected, session.Status, "FR-09")
}

func TestAgentTurnDegradedWhenAIFails(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	ai := &fakeAgentAI{err: fmt.Errorf("gemini down")}
	r := agentRouter(db, ai, tenant.ID)

	agentPost(t, r, "/turn", map[string]any{})
	agentPost(t, r, "/turn", map[string]any{"text": "La Esquina"})
	code, data := agentPost(t, r, "/turn", map[string]any{"text": "tengo una tienda"})
	require.Equal(t, http.StatusOK, code, "AI failure is NEVER a 5xx (AC-10)")
	assert.Equal(t, true, data["degraded"])
	assert.Equal(t, true, data["offer_fallback"])

	// Session stays active so the tendero can retry or fall back.
	var session models.AgentSession
	require.NoError(t, db.Where("tenant_id = ? AND status = ?", tenant.ID, "active").First(&session).Error)
}

func TestAgentTurnBudgetExhausted(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	ai := &fakeAgentAI{extraction: tiendaLicoresExtraction()}
	r := agentRouter(db, ai, tenant.ID)

	agentPost(t, r, "/turn", map[string]any{})
	agentPost(t, r, "/turn", map[string]any{"text": "La Esquina"})
	require.NoError(t, db.Model(&models.AgentSession{}).
		Where("tenant_id = ?", tenant.ID).
		Update("model_calls", services.MaxAgentModelCalls).Error)

	code, data := agentPost(t, r, "/turn", map[string]any{"text": "tengo una tienda"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, data["degraded"])
	assert.Equal(t, "budget", data["reason"], "AC-14: hard budget cap")
	assert.Equal(t, 0, ai.calls, "no model call beyond the budget")
}

func TestAgentTenantIsolation(t *testing.T) {
	db := setupTestDB(t)
	tenantA := createAgentTestTenant(t, db)
	tenantB := createAgentTestTenant(t, db)
	rA := agentRouter(db, &fakeAgentAI{}, tenantA.ID)
	rB := agentRouter(db, &fakeAgentAI{}, tenantB.ID)

	_, dataA := agentPost(t, rA, "/turn", map[string]any{})
	sessionA := dataA["session_id"].(string)

	// B's turn creates its OWN session (AC-13).
	_, dataB := agentPost(t, rB, "/turn", map[string]any{})
	assert.NotEqual(t, sessionA, dataB["session_id"])

	// B cannot confirm A's session.
	code, _ := agentPost(t, rB, "/confirm", map[string]any{"session_id": sessionA})
	assert.Equal(t, http.StatusNotFound, code)
}

func TestAgentFallbackAppliesProfile(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	r := agentRouter(db, &fakeAgentAI{}, tenant.ID)

	code, data := agentPost(t, r, "/fallback", map[string]any{
		"types": []string{models.BusinessTypePeluqueria, models.BusinessTypeTiendaBarrio},
		"attrs": map[string]bool{"equipo": true},
	})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, data["onboarding_completed"])

	var fresh models.Tenant
	require.NoError(t, db.Where("id = ?", tenant.ID).First(&fresh).Error)
	assert.True(t, fresh.OnboardingCompleted)
	assert.Equal(t, []string{models.BusinessTypePeluqueria, models.BusinessTypeTiendaBarrio}, fresh.BusinessTypes)
	assert.True(t, fresh.FeatureFlags.EnableServices)

	var session models.AgentSession
	require.NoError(t, db.Where("tenant_id = ?", tenant.ID).First(&session).Error)
	assert.Equal(t, models.AgentSessionStatusFallback, session.Status)
}

func TestAgentFallbackRejectsInvalidType(t *testing.T) {
	db := setupTestDB(t)
	tenant := createAgentTestTenant(t, db)
	r := agentRouter(db, &fakeAgentAI{}, tenant.ID)

	code, _ := agentPost(t, r, "/fallback", map[string]any{"types": []string{"hackear_sistema"}})
	assert.Equal(t, http.StatusBadRequest, code)
}
