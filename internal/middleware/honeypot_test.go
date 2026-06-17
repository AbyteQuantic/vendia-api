// Spec: specs/064-anti-bot-honeypot/spec.md
package middleware_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildHoneypotRouter monta el middleware con un handler dummy que, si se
// ejecuta, devuelve 200 + el body que recibió (para verificar la restitución).
func buildHoneypotRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/x", middleware.HoneypotMiddleware(), func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		c.JSON(http.StatusOK, gin.H{"got": string(b)})
	})
	return r
}

func postJSON(r *gin.Engine, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHoneypot_PassesCleanRequest(t *testing.T) {
	r := buildHoneypotRouter()
	w := postJSON(r, map[string]any{"customer_name": "Ana", "website": ""})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestHoneypot_PassesWhenFieldAbsent(t *testing.T) {
	r := buildHoneypotRouter()
	w := postJSON(r, map[string]any{"customer_name": "Ana"})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestHoneypot_RejectsFilledTrap(t *testing.T) {
	r := buildHoneypotRouter()
	w := postJSON(r, map[string]any{"customer_name": "Bot", "website": "http://spam.example"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHoneypot_RejectsInstantSubmit(t *testing.T) {
	r := buildHoneypotRouter()
	w := postJSON(r, map[string]any{"customer_name": "Bot", "form_elapsed_ms": 120})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHoneypot_AllowsHumanPacedSubmit(t *testing.T) {
	r := buildHoneypotRouter()
	w := postJSON(r, map[string]any{"customer_name": "Ana", "form_elapsed_ms": 5000})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestHoneypot_RestoresBodyForHandler(t *testing.T) {
	r := buildHoneypotRouter()
	w := postJSON(r, map[string]any{"customer_name": "Ana", "website": ""})
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// El handler debe haber recibido el body completo intacto.
	assert.Contains(t, resp["got"], "customer_name")
	assert.Contains(t, resp["got"], "Ana")
}

func TestHoneypot_EmptyBodyPasses(t *testing.T) {
	r := buildHoneypotRouter()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
