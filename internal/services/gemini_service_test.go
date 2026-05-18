package services

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Spec: specs/015-ia-foto-timeouts/spec.md — FR-03 / D2.
// requestContext must HONOR the caller's context deadline instead of
// silently shrinking it back to s.timeout. The bug: image handlers
// grant ~110s of context, but every Gemini call did
// context.WithTimeout(ctx, s.timeout) with s.timeout=30s — so the
// HTTP call to Gemini died at 30s even though an image operation
// needs ~27s + download + upload. The fix: when the caller already
// carries a deadline, defer to it; only fall back to s.timeout when
// the caller passed a context with no deadline.
func TestRequestContext_HonorsCallerDeadline(t *testing.T) {
	svc := &GeminiService{timeout: 30 * time.Second}

	t.Run("caller deadline longer than s.timeout is preserved", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), 110*time.Second)
		defer cancelParent()

		ctx, cancel := svc.requestContext(parent)
		defer cancel()

		dl, ok := ctx.Deadline()
		assert.True(t, ok, "derived context must carry a deadline")
		remaining := time.Until(dl)
		assert.Greater(t, remaining, 31*time.Second,
			"the caller's 110s deadline must NOT be shrunk to s.timeout (30s)")
		assert.LessOrEqual(t, remaining, 110*time.Second,
			"derived deadline must not exceed the caller's deadline")
	})

	t.Run("caller deadline shorter than s.timeout is preserved", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelParent()

		ctx, cancel := svc.requestContext(parent)
		defer cancel()

		dl, ok := ctx.Deadline()
		assert.True(t, ok, "derived context must carry a deadline")
		remaining := time.Until(dl)
		assert.LessOrEqual(t, remaining, 5*time.Second,
			"a caller with a tight deadline must not be expanded to s.timeout")
	})

	t.Run("context without deadline falls back to s.timeout", func(t *testing.T) {
		ctx, cancel := svc.requestContext(context.Background())
		defer cancel()

		dl, ok := ctx.Deadline()
		assert.True(t, ok, "fallback must apply s.timeout when caller has no deadline")
		remaining := time.Until(dl)
		assert.Greater(t, remaining, 25*time.Second,
			"fallback timeout should be close to s.timeout (30s)")
		assert.LessOrEqual(t, remaining, 30*time.Second,
			"fallback timeout must not exceed s.timeout")
	})

	t.Run("cancelling the derived context propagates", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), 110*time.Second)
		defer cancelParent()

		ctx, cancel := svc.requestContext(parent)
		cancel()
		assert.Error(t, ctx.Err(), "cancel() must cancel the derived context")
	})
}

func TestNewGeminiService_Defaults(t *testing.T) {
	// With an unconfigured imageModel, the constructor falls through
	// discovery (which fails with the bogus key) and lands on the
	// hardcoded defaults. Pinning these IDs here protects against an
	// accidental rename of the constants — Render relies on the
	// fallback during cold starts before the env var is plumbed in.
	svc := NewGeminiService("test-key", "", "", 0)
	assert.NotNil(t, svc)
	assert.Equal(t, "gemini-2.0-flash", svc.model)
	assert.Equal(t, "gemini-3-pro-image-preview", svc.imageModel,
		"default image model must be Nano Banana Pro for product-photo "+
			"identity preservation")
	assert.Equal(t, 30*time.Second, svc.timeout)
	assert.Equal(t, "test-key", svc.apiKey)
}

func TestDefaultImageModel_IsNanoBananaPro(t *testing.T) {
	// Mirror the const directly so refactors that touch the constant
	// have to update this test deliberately.
	assert.Equal(t, "gemini-3-pro-image-preview", defaultImageModel,
		"default image model is the contract — change deliberately")
	assert.Equal(t, "gemini-2.0-flash", defaultTextModel,
		"default text model unchanged — text/OCR still runs on Flash")
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
		name         string
		businessType string
		details      string
		mustContain  []string
	}{
		{
			name:         "empty details falls back to rubro default",
			businessType: "tienda_barrio",
			details:      "",
			mustContain:  []string{"corner storefront"},
		},
		{
			name:         "ice cream keyword wins",
			businessType: "tienda_barrio",
			details:      "Tienda con helados artesanales de frutas",
			mustContain:  []string{"ice cream cone"},
		},
		{
			name:         "the case from the demo phone — llaveros + moda",
			businessType: "emprendimiento_general",
			details:      "Llaveros y utensilios de moda",
			mustContain:  []string{"key-ring", "hanger"},
		},
		{
			name:         "single match returns single subject (no together-with chain)",
			businessType: "comidas_rapidas",
			details:      "vendo hamburguesas a domicilio",
			mustContain:  []string{"hamburger"},
		},
		{
			name:         "no keyword match falls back to rubro default",
			businessType: "manufactura",
			details:      "We make custom-engineered widgets",
			mustContain:  []string{"gear"},
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
