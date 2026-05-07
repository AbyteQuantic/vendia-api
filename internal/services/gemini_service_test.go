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

// buildEnhancePhotoPrompt is the hot path for "Mejorar con IA" on
// product photos. The previous prompt only forbade colour changes;
// production users reported the model regenerating recognisable
// products (e.g. a Toy Story keychain) from its training prior,
// losing the actual SKU's silhouette and accessories. The current
// prompt pins identity preservation explicitly. These tests pin the
// regression so a future "tighten the prompt" refactor can't
// accidentally drop the anti-regeneration anchors.
func TestBuildEnhancePhotoPrompt_IdentityPreservationAnchors(t *testing.T) {
	prompt := buildEnhancePhotoPrompt("")

	// Anti-regeneration anchor: the model must NOT fall back to its
	// training-time mental model of recognisable products.
	mustContain := []string{
		"FUENTE CANÓNICA",
		"NO uses tu conocimiento previo",
		"IGNORA ese conocimiento",
		"PIXEL-A-PIXEL la silueta",
		"fidelidad a la foto",
	}
	for _, s := range mustContain {
		assert.Contains(t, prompt, s,
			"identity-preservation anchor missing: %q", s)
	}

	// Negative list: the specific failure modes the QA team caught
	// (Toy Story keychain regenerated with different face / base /
	// keyring position) all need their explicit prohibition.
	negativeList := []string{
		"PROHIBIDO redibujar la cara",
		"PROHIBIDO mover, duplicar, eliminar o reposicionar accesorios",
		"PROHIBIDO sustituir la base/pies/soporte",
	}
	for _, s := range negativeList {
		assert.Contains(t, prompt, s,
			"negative prompt missing: %q", s)
	}

	// Self-verification block: the model is asked to re-check its
	// own output before delivering, which empirically reduces the
	// regeneration rate on diffusion models.
	assert.Contains(t, prompt, "VERIFICACIÓN ANTES DE ENTREGAR",
		"self-check block missing — regression risk")
	assert.Contains(t, prompt, "¿La silueta del producto coincide con la entrada?",
		"silhouette check missing")
}

func TestBuildEnhancePhotoPrompt_ProductInfoIsInjected(t *testing.T) {
	prompt := buildEnhancePhotoPrompt("Llavero Alien Pixar verde con aro metálico")
	assert.Contains(t, prompt, "El producto es: Llavero Alien Pixar verde con aro metálico.",
		"productInfo must be embedded so the model knows the SKU context")
}

func TestBuildEnhancePhotoPrompt_EmptyProductInfoStillBuilds(t *testing.T) {
	// Empty productInfo is a valid input — the call sites pass "" when
	// the merchant hasn't filled in the product name yet. The prompt
	// must still build a complete, working instruction without trailing
	// whitespace artifacts that confuse the diffusion model.
	prompt := buildEnhancePhotoPrompt("")
	assert.NotContains(t, prompt, "El producto es: .",
		"empty productInfo must not leave a dangling sentence")
	assert.Contains(t, prompt, "Eres un EDITOR FOTOGRÁFICO profesional",
		"prompt header must always be present")
}

func TestBuildEnhancePhotoPrompt_ColorAndFramingRulesPreserved(t *testing.T) {
	// The original prompt's framing rules (1:1, 75% max area, 12% safe
	// zone, white background) were proven in production — the new
	// identity layer must add to them, not replace them.
	prompt := buildEnhancePhotoPrompt("")
	for _, anchor := range []string{
		"colores originales son SAGRADOS",
		"BLANCO PURO sólido (#FFFFFF)",
		"Formato cuadrado 1:1",
		"75% del área", // Sprintf collapses %% in the source to a single % in output.
		"safe zone",
	} {
		assert.Contains(t, prompt, anchor,
			"legacy framing rule lost during refactor: %q", anchor)
	}
}
