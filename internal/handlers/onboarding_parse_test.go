// Spec: specs/045-onboarding-agentic/onboarding_agentic_spec.md
package handlers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doOnboardingParse(t *testing.T, gemini *services.GeminiService, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	require.NoError(t, mw.Close())

	r := gin.New()
	r.POST("/auth/onboarding-parse", OnboardingParse(gemini))
	req := httptest.NewRequest(http.MethodPost, "/auth/onboarding-parse", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Sin IA configurada → 200 degraded:true (NUNCA 500) para que el front
// degrade a edición manual (D5).
func TestOnboardingParse_NilService_Degraded(t *testing.T) {
	w := doOnboardingParse(t, nil, map[string]string{"text": "soy María, tienda"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			Degraded bool   `json:"degraded"`
			Reason   string `json:"reason"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Data.Degraded)
	assert.Equal(t, "ai_unavailable", resp.Data.Reason)
}

// Sin texto ni audio → 200 degraded:empty (no hay nada que extraer).
func TestOnboardingParse_EmptyInput_Degraded(t *testing.T) {
	gemini := services.NewGeminiService("k", "m", "im", time.Second)
	w := doOnboardingParse(t, gemini, map[string]string{"text": "   "})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			Degraded bool   `json:"degraded"`
			Reason   string `json:"reason"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Data.Degraded)
	assert.Equal(t, "empty", resp.Data.Reason)
}

// computeNeedsConfirmation: un campo detectado con confianza < umbral entra a
// needs_confirmation; con confianza alta NO; sin confianza reportada NO.
func TestComputeNeedsConfirmation_Thresholds(t *testing.T) {
	bt := "tienda_barrio"
	addr := "Calle 5"
	name := "María"
	f := services.OnboardingFields{
		BusinessType: &bt,   // umbral 0.85
		Address:      &addr, // umbral 0.6
		OwnerName:    &name, // umbral 0.7
	}
	conf := map[string]float64{
		"business_type": 0.80, // < 0.85 → confirmar
		"address":       0.65, // > 0.6  → pasa
		"owner_name":    0.50, // < 0.7  → confirmar
	}
	got := computeNeedsConfirmation(f, conf)
	assert.ElementsMatch(t, []string{"business_type", "owner_name"}, got)
}
