package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewGeminiService_Defaults(t *testing.T) {
	svc := NewGeminiService("test-key", "", "", 0)
	assert.NotNil(t, svc)
	assert.Equal(t, "gemini-2.0-flash", svc.model)
	assert.Equal(t, "gemini-2.5-flash-image", svc.imageModel)
	assert.Equal(t, 30*time.Second, svc.timeout)
	assert.Equal(t, "test-key", svc.apiKey)
}

func TestNewGeminiService_CustomModel(t *testing.T) {
	svc := NewGeminiService("key", "gemini-pro", "custom-image", 60*time.Second)
	assert.Equal(t, "gemini-pro", svc.model)
	assert.Equal(t, "custom-image", svc.imageModel)
	assert.Equal(t, 60*time.Second, svc.timeout)
}

// resolveLogoSubject is the contract that turns the merchant's free
// text into a concrete pictogram brief for the image-gen prompt. The
// behaviour matters because the model drifts hard when given vague
// or competing options — the test pins the keyword mapping for the
// patterns we've already seen burn in production.
func TestResolveLogoSubject_KeywordMapping(t *testing.T) {
	cases := []struct {
		name        string
		businessType string
		details     string
		mustContain []string
	}{
		{
			name:        "empty details falls back to rubro default",
			businessType: "tienda_barrio",
			details:     "",
			mustContain: []string{"corner storefront"},
		},
		{
			name:        "ice cream keyword wins",
			businessType: "tienda_barrio",
			details:     "Tienda con helados artesanales de frutas",
			mustContain: []string{"ice cream cone"},
		},
		{
			name:        "the case from the demo phone — llaveros + moda",
			businessType: "emprendimiento_general",
			details:     "Llaveros y utensilios de moda",
			mustContain: []string{"key-ring", "hanger"},
		},
		{
			name:        "single match returns single subject (no together-with chain)",
			businessType: "comidas_rapidas",
			details:     "vendo hamburguesas a domicilio",
			mustContain: []string{"hamburger"},
		},
		{
			name:        "no keyword match falls back to rubro default",
			businessType: "manufactura",
			details:     "We make custom-engineered widgets",
			mustContain: []string{"gear"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := resolveLogoSubject(c.businessType, c.details)
			for _, s := range c.mustContain {
				assert.Contains(t, out, s,
					"subject %q should mention %q for details=%q",
					out, s, c.details)
			}
		})
	}
}
