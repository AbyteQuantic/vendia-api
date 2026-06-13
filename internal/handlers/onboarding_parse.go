// Spec: specs/045-onboarding-agentic/onboarding_agentic_spec.md
package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
)

// onboardingParseResponse is the wire contract the Flutter onboarding reads.
// Always 200 — the IA is an OPTIONAL accelerator; it NEVER blocks the
// registration. When unavailable/failed, degraded=true + a reason lets the
// front fall back to filling the Smart Cards by hand (Art. I + II).
type onboardingParseResponse struct {
	Fields            services.OnboardingFields `json:"fields"`
	Confidence        map[string]float64        `json:"confidence"`
	NeedsConfirmation []string                  `json:"needs_confirmation"`
	ClarifyPrompt     *string                   `json:"clarify_prompt"`
	Degraded          bool                      `json:"degraded"`
	Reason            *string                   `json:"reason,omitempty"`
}

func degradedOnboarding(reason string) onboardingParseResponse {
	return onboardingParseResponse{
		Fields:            services.OnboardingFields{},
		Confidence:        map[string]float64{},
		NeedsConfirmation: []string{},
		ClarifyPrompt:     nil,
		Degraded:          true,
		Reason:            &reason,
	}
}

// OnboardingParse — POST /api/v1/auth/onboarding-parse (PÚBLICO, pre-auth,
// stateless). Extrae campos del onboarding desde texto y/o una nota de voz.
// NO crea el tenant. Espejo del patrón de voice-inventory; rate-limit por IP.
func OnboardingParse(geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Nil-safe: sin IA configurada → 200 degraded, NUNCA 500 (D5).
		if geminiSvc == nil {
			c.JSON(http.StatusOK, gin.H{"data": degradedOnboarding("ai_unavailable")})
			return
		}

		text := strings.TrimSpace(c.PostForm("text"))
		current := c.PostForm("current")

		var audioData []byte
		var mimeType string
		if file, header, err := c.Request.FormFile("audio"); err == nil {
			defer file.Close()
			if header.Size > 10<<20 {
				c.JSON(http.StatusOK, gin.H{"data": degradedOnboarding("unsupported_audio")})
				return
			}
			mimeType = header.Header.Get("Content-Type")
			data, readErr := io.ReadAll(file)
			if readErr != nil {
				c.JSON(http.StatusOK, gin.H{"data": degradedOnboarding("unsupported_audio")})
				return
			}
			audioData = data
		}

		// Sin texto ni audio no hay nada que extraer.
		if text == "" && len(audioData) == 0 {
			c.JSON(http.StatusOK, gin.H{"data": degradedOnboarding("empty")})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
		defer cancel()

		out, err := geminiSvc.ExtractOnboardingFields(ctx, text, audioData, mimeType, current)
		if err != nil {
			reason := "timeout"
			if strings.Contains(err.Error(), "unsupported audio") {
				reason = "unsupported_audio"
			}
			c.JSON(http.StatusOK, gin.H{"data": degradedOnboarding(reason)})
			return
		}

		// Defensas deterministas server-side (no confiar solo en el prompt):
		// whitelist de business_type, enum de logo_intent, normalización phone.
		services.SanitizeOnboardingFields(&out.Fields)

		resp := onboardingParseResponse{
			Fields:            out.Fields,
			Confidence:        out.Confidence,
			NeedsConfirmation: computeNeedsConfirmation(out.Fields, out.Confidence),
			ClarifyPrompt:     out.ClarifyPrompt,
			Degraded:          false,
		}
		c.JSON(http.StatusOK, gin.H{"data": resp})
	}
}

// computeNeedsConfirmation is the server-side backstop for low-confidence
// fields (D8): a DETECTED (non-nil) field whose confidence is below its
// per-field threshold goes to needs_confirmation so the front does NOT
// auto-fill it (and asks again with chips). Deterministic → unit-testable.
func computeNeedsConfirmation(f services.OnboardingFields, conf map[string]float64) []string {
	detected := map[string]bool{
		"owner_name":            f.OwnerName != nil,
		"owner_last_name":       f.OwnerLastName != nil,
		"phone":                 f.Phone != nil,
		"business_name":         f.BusinessName != nil,
		"razon_social":          f.RazonSocial != nil,
		"nit":                   f.NIT != nil,
		"address":               f.Address != nil,
		"business_type":         f.BusinessType != nil,
		"has_multiple_branches": f.HasMultipleBranches != nil,
		"offers_services":       f.OffersServices != nil,
		"sells_by_weight":       f.SellsByWeight != nil,
		"has_tables":            f.HasTables != nil,
		"logo_intent":           f.LogoIntent != nil,
		"has_employees":         f.HasEmployees != nil,
	}
	out := []string{}
	for field, isSet := range detected {
		if !isSet {
			continue
		}
		confidence, ok := conf[field]
		if !ok {
			// Sin confianza reportada: no es backstop — el prompt ya hace
			// null a lo dudoso. Lo dejamos pasar.
			continue
		}
		if confidence < services.OnboardingConfidenceThreshold(field) {
			out = append(out, field)
		}
	}
	return out
}
