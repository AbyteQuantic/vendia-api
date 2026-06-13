// Spec: specs/045-onboarding-agentic/onboarding_agentic_spec.md
package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func obStr(s string) *string { return &s }

func TestSanitizeOnboardingFields_BusinessTypeWhitelist(t *testing.T) {
	valid := obStr("tienda_barrio")
	f := &OnboardingFields{BusinessType: valid}
	SanitizeOnboardingFields(f)
	assert.NotNil(t, f.BusinessType)
	assert.Equal(t, "tienda_barrio", *f.BusinessType)

	// Enum fuera de la whitelist → forzado a nil (D9).
	f2 := &OnboardingFields{BusinessType: obStr("algo_inventado")}
	SanitizeOnboardingFields(f2)
	assert.Nil(t, f2.BusinessType)
}

func TestSanitizeOnboardingFields_PhoneNormalization(t *testing.T) {
	f := &OnboardingFields{Phone: obStr("+57 300 123 4567")}
	SanitizeOnboardingFields(f)
	if assert.NotNil(t, f.Phone) {
		assert.Equal(t, "3001234567", *f.Phone) // 10 dígitos, sin +57 ni espacios
	}

	f2 := &OnboardingFields{Phone: obStr("abc")}
	SanitizeOnboardingFields(f2)
	assert.Nil(t, f2.Phone) // sin dígitos → nil
}

func TestSanitizeOnboardingFields_LogoIntentEnum(t *testing.T) {
	f := &OnboardingFields{LogoIntent: obStr("Generar")}
	SanitizeOnboardingFields(f)
	if assert.NotNil(t, f.LogoIntent) {
		assert.Equal(t, "generar", *f.LogoIntent)
	}

	f2 := &OnboardingFields{LogoIntent: obStr("cualquier_cosa")}
	SanitizeOnboardingFields(f2)
	assert.Nil(t, f2.LogoIntent)
}

func TestOnboardingConfidenceThreshold_PerField(t *testing.T) {
	assert.InDelta(t, 0.85, OnboardingConfidenceThreshold("business_type"), 1e-9)
	assert.InDelta(t, 0.6, OnboardingConfidenceThreshold("address"), 1e-9)
	assert.InDelta(t, 0.7, OnboardingConfidenceThreshold("phone"), 1e-9)
}

// El prompt es el contrato con el mapeo Flutter — pinéalo contra drift.
func TestOnboardingParsePrompt_Contract(t *testing.T) {
	for _, must := range []string{
		"tienda_barrio", "academias_instituciones",
		"IGNORA", "PIN", "10 dígitos", "null", "logo_intent",
	} {
		assert.True(t, strings.Contains(OnboardingParsePrompt, must),
			"el prompt debe mencionar %q", must)
	}
}
