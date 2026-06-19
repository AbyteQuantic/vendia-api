// Spec: specs/067-planear-menu-ia-ux/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// El anti-alucinación REAL vive en parseSuggestedDays: descarta cualquier
// recipe_uuid que NO esté en la whitelist re-derivada del tenant.
func TestParseSuggestedDays_OnlyWhitelistedUUIDs(t *testing.T) {
	allowed := map[string]struct{}{"r-real": {}}
	// Gemini propone un día con un uuid VÁLIDO y uno INVENTADO.
	text := `{"days":{"mon":{"enabled":true,"items":[` +
		`{"recipe_uuid":"r-real","planned_qty":0},` +
		`{"recipe_uuid":"r-fantasma","planned_qty":0}]}}}`

	days := parseSuggestedDays(text, allowed)

	mon := days["mon"]
	assert.True(t, mon.Enabled)
	assert.Len(t, mon.Items, 1, "el uuid inventado debe descartarse")
	assert.Equal(t, "r-real", mon.Items[0].RecipeUUID)
}

func TestParseSuggestedDays_DropsInvalidWeekday(t *testing.T) {
	allowed := map[string]struct{}{"r1": {}}
	text := `{"days":{"funday":{"enabled":true,"items":[{"recipe_uuid":"r1","planned_qty":0}]}}}`

	days := parseSuggestedDays(text, allowed)

	_, ok := days["funday"]
	assert.False(t, ok, "una clave de día inválida se descarta")
}

func TestParseSuggestedDays_DisablesDayWhenAllRejected(t *testing.T) {
	allowed := map[string]struct{}{"r1": {}}
	// Todos los uuids del día son inventados → día queda apagado y vacío.
	text := `{"days":{"tue":{"enabled":true,"items":[{"recipe_uuid":"x","planned_qty":0}]}}}`

	days := parseSuggestedDays(text, allowed)

	tue := days["tue"]
	assert.False(t, tue.Enabled)
	assert.Empty(t, tue.Items)
}

func TestParseSuggestedDays_StripsMarkdownFence(t *testing.T) {
	allowed := map[string]struct{}{"r1": {}}
	text := "```json\n{\"days\":{\"wed\":{\"enabled\":true,\"items\":[{\"recipe_uuid\":\"r1\",\"planned_qty\":2}]}}}\n```"

	days := parseSuggestedDays(text, allowed)

	wed := days["wed"]
	assert.True(t, wed.Enabled)
	assert.Len(t, wed.Items, 1)
	assert.Equal(t, 2, wed.Items[0].PlannedQty)
}

func TestParseSuggestedDays_NegativeQtyClampedToZero(t *testing.T) {
	allowed := map[string]struct{}{"r1": {}}
	text := `{"days":{"fri":{"enabled":true,"items":[{"recipe_uuid":"r1","planned_qty":-5}]}}}`

	days := parseSuggestedDays(text, allowed)

	assert.Equal(t, 0, days["fri"].Items[0].PlannedQty)
}

func TestSuggestMenuPlan_NilGemini503(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(fakeTenant("t1"))
	r.POST("/menu-plan/suggest", SuggestMenuPlan(db, nil))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/menu-plan/suggest", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSuggestMenuPlan_TooFewRecipes422(t *testing.T) {
	db := setupMenuPlanTestDB()
	// Solo 1 receta para el tenant (umbral mínimo = 3).
	db.Create(&models.Recipe{TenantID: "t1", ProductName: "Sopa", SalePrice: 1000})

	// GeminiService NO-nil pero sin apiKey: el corte temprano debe responder
	// 422 ANTES de intentar llamar a Gemini.
	gem := services.NewGeminiService("", "gemini", "imagen", time.Second)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(fakeTenant("t1"))
	r.POST("/menu-plan/suggest", SuggestMenuPlan(db, gem))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/menu-plan/suggest", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	assert.Contains(t, body["error"], "recetas")
}
