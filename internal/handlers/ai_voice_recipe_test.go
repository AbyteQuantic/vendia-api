// Spec: specs/065-recipe-studio/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func mountVoiceRecipe() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// svc is nil in these guard tests; the handler must fail closed.
	r.POST("/ai/voice-recipe", handlers.VoiceRecipe(nil))
	r.POST("/ai/recipe-assist", handlers.RecipeAssist(nil))
	return r
}

func TestVoiceRecipe_RejectsWhenServiceUnconfigured(t *testing.T) {
	r := mountVoiceRecipe()
	// minimal multipart with an audio_file so we reach the nil-svc guard
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("audio_file", "a.webm")
	_, _ = fw.Write([]byte("xx"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/ai/voice-recipe", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

func TestRecipeAssist_RejectsWhenServiceUnconfigured(t *testing.T) {
	r := mountVoiceRecipe()
	body, _ := json.Marshal(map[string]any{"name": "Arroz"})
	req := httptest.NewRequest(http.MethodPost, "/ai/recipe-assist", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}
