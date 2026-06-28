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

// Spec: specs/017-ia-mejora-fiel-producto/spec.md — FR-01, FR-03.
// buildEnhancePhotoPrompt is the hot path for "Mejorar con IA" on
// product photos. The previous prompt induced the model to REGENERATE
// the product from its name/description (productInfo) instead of
// EDITING the attached photo: a merchant photographed a specific
// Kuromi-character keychain and the AI returned generic metal
// keychains in a bag, because the prompt told the model to "create an
// image of {productInfo}". The rewritten prompt is a strict faithful
// product-photography EDIT instruction: the attached image IS the
// product and the sole source of truth; the model may only cut the
// product from its background and place it on pure white. These tests
// pin that contract so a future refactor can't reintroduce the
// regeneration behaviour.
func TestBuildEnhancePhotoPrompt_FidelityAnchors(t *testing.T) {
	prompt := buildEnhancePhotoPrompt("")

	// FR-01: the attached photo is declared the single source of truth.
	sourceOfTruth := []string{
		"THE ATTACHED IMAGE IS THE PRODUCT",
		"one and only source of truth",
		"EDIT the attached photograph",
	}
	for _, s := range sourceOfTruth {
		assert.Contains(t, prompt, s,
			"source-of-truth anchor missing: %q", s)
	}

	// FR-01: regeneration / substitution must be explicitly forbidden.
	prohibitions := []string{
		"DO NOT replace",
		"DO NOT redesign",
		"DO NOT reinvent",
		"DO NOT generate a different",
		"DO NOT add or remove",
	}
	for _, s := range prohibitions {
		assert.Contains(t, prompt, s,
			"prohibition anchor missing: %q", s)
	}

	// FR-02: the result must be a clean white-background catalog photo.
	whiteBg := []string{
		"pure white background",
		"studio",
		"shadow",
	}
	for _, s := range whiteBg {
		assert.Contains(t, prompt, s,
			"white-background / catalog-quality anchor missing: %q", s)
	}

	// FR-03: the output product must be recognisably the same SKU.
	assert.Contains(t, prompt, "recognisably the same product",
		"recognisability anchor missing — regression risk")

	// "When in doubt, keep exactly what the photo shows."
	assert.Contains(t, prompt, "When in doubt",
		"doubt-resolution anchor missing")

	// Mejora de imperfecciones + calidad SIN tocar el producto (pedido del
	// fundador 2026-06-28): limpiar foto/suciedad y subir calidad, pero la
	// distinción crítica protege que no se rediseñe el producto.
	qualityAnchors := []string{
		"remove dust, dirt",      // imperfecciones de superficie
		"glare",                  // reflejos molestos
		"remove blur",            // nitidez
		"noise",                  // ruido digital
		"HIGH-RESOLUTION",        // calidad de salida
		"WITHOUT inventing any detail", // no alucinar detalles
		"CRITICAL DISTINCTION",   // foto sí / producto no
	}
	for _, s := range qualityAnchors {
		assert.Contains(t, prompt, s, "quality/imperfection anchor missing: %q", s)
	}
}

// FR-04: productInfo must be a CONTEXT HINT only — never a generation
// target. The prompt phrases it as "the product is a {productInfo}"
// and never as "create/generate an image of {productInfo}".
func TestBuildEnhancePhotoPrompt_ProductInfoIsHintOnly(t *testing.T) {
	prompt := buildEnhancePhotoPrompt("Llavero Kuromi verde con aro metálico")

	assert.Contains(t, prompt,
		"For context only, the product is a Llavero Kuromi verde con aro metálico",
		"productInfo must be embedded as a context hint")

	// Negative: productInfo must never be wired as a generation target.
	assert.NotContains(t, prompt, "generate an image of Llavero Kuromi",
		"productInfo must not be a generation target")
	assert.NotContains(t, prompt, "create a photo of Llavero Kuromi",
		"productInfo must not be a generation target")
}

func TestBuildEnhancePhotoPrompt_EmptyProductInfoStillBuilds(t *testing.T) {
	// Empty productInfo is a valid input — the call sites pass "" when
	// the merchant hasn't filled in the product name yet. The prompt
	// must still build a complete, working instruction without trailing
	// whitespace artifacts that confuse the diffusion model.
	prompt := buildEnhancePhotoPrompt("")
	assert.NotContains(t, prompt, "the product is a .",
		"empty productInfo must not leave a dangling sentence")
	assert.NotContains(t, prompt, "For context only",
		"empty productInfo must not emit the context-hint sentence")
	assert.Contains(t, prompt, "THE ATTACHED IMAGE IS THE PRODUCT",
		"prompt body must always be present")
}

func TestBuildEnhancePhotoPrompt_FramingRulesPreserved(t *testing.T) {
	// The framing rules (white background, soft shadow, centered,
	// e-commerce catalog quality) must always be present so the edit
	// produces a usable catalog photo.
	prompt := buildEnhancePhotoPrompt("")
	for _, anchor := range []string{
		"pure white background",
		"centered",
		"e-commerce",
	} {
		assert.Contains(t, prompt, anchor,
			"framing rule lost during refactor: %q", anchor)
	}
}

// Spec: specs/021-ia-generacion-respeta-tipo/spec.md — FR-01..FR-04.
// buildGenerateProductPrompt is the prompt used by "Generar foto con
// IA" — generating a catalog photo for a product that has NO source
// photo, from its name alone. The bug it fixes: a "Llavero Hello
// Kitty" with presentation "Bolsa" generated a Hello Kitty PURSE, not
// a keychain. Two causes — the prompt let the famous character
// ("Hello Kitty") outweigh the product type ("Llavero"), and the raw
// presentation ("Bolsa" = packaging) leaked into the object text so
// the model drew a bag. The rewritten prompt declares the product
// TYPE (main noun of the name) as the physical object, the
// brand/character as decoration only, and the presentation as
// packaging context that must NEVER be drawn as the object. These
// tests pin that contract so a refactor can't reintroduce the bug.
func TestBuildGenerateProductPrompt_TypeIsTheObject(t *testing.T) {
	prompt := buildGenerateProductPrompt("Llavero Hello Kitty", "Bolsa")

	// FR-01: the product TYPE — main noun of the name — is the object.
	typeAnchors := []string{
		"main noun",
		"physical object",
		"TYPE of product",
	}
	for _, s := range typeAnchors {
		assert.Contains(t, prompt, s,
			"product-type-is-the-object anchor missing: %q", s)
	}

	// FR-02: the brand/character is decoration only, never the object.
	themeAnchors := []string{
		"brand or character",
		"theme",
		"decoration",
	}
	for _, s := range themeAnchors {
		assert.Contains(t, prompt, s,
			"brand-is-only-theme anchor missing: %q", s)
	}
	// The famous-character merch trap must be explicitly forbidden.
	assert.Contains(t, prompt, "purse",
		"prompt must explicitly forbid generating a purse/wallet/plush")
	assert.Contains(t, prompt, "wallet",
		"prompt must explicitly forbid generating generic character merch")

	// FR-03: presentation is packaging/context — forbidden as the object.
	packagingAnchors := []string{
		"packaging",
		"NOT the object",
	}
	for _, s := range packagingAnchors {
		assert.Contains(t, prompt, s,
			"presentation-is-packaging anchor missing: %q", s)
	}

	// FR-04: catalog-quality white-background result.
	for _, s := range []string{"white", "e-commerce", "centered"} {
		assert.Contains(t, prompt, s,
			"catalog-quality anchor missing: %q", s)
	}
}

// FR-01/FR-03: the product name and the presentation must reach the
// model as SEPARATE, LABELLED fields — never concatenated into one
// object phrase. With name "Llavero Hello Kitty" + presentation
// "Bolsa", the model must see the type as the object and "Bolsa"
// flagged as packaging, so it never reads "Llavero ... Bolsa" as one
// object and draws a bag.
func TestBuildGenerateProductPrompt_PresentationIsLabelledSeparately(t *testing.T) {
	prompt := buildGenerateProductPrompt("Llavero Hello Kitty", "Bolsa")

	// The name is the object brief.
	assert.Contains(t, prompt, "Llavero Hello Kitty",
		"the product name must appear as the object brief")
	// The presentation appears flagged as packaging — never glued to
	// the name as part of the object phrase.
	assert.Contains(t, prompt, "Bolsa",
		"the presentation must appear, labelled as packaging")
	assert.NotContains(t, prompt, "Llavero Hello Kitty Bolsa",
		"name and presentation must NOT be concatenated into one object phrase")
}

// FR-03: when there is no presentation, the prompt must still build a
// complete instruction with no dangling packaging sentence.
func TestBuildGenerateProductPrompt_EmptyPresentation(t *testing.T) {
	prompt := buildGenerateProductPrompt("Camiseta Coca-Cola", "")

	assert.Contains(t, prompt, "Camiseta Coca-Cola",
		"the product name must always be present")
	assert.NotContains(t, prompt, "PACKAGING CONTEXT:",
		"with no presentation, the packaging line must not be emitted")
	// Core type/theme anchors must survive regardless of presentation.
	assert.Contains(t, prompt, "main noun",
		"product-type anchor must always be present")
	assert.Contains(t, prompt, "white",
		"white-background anchor must always be present")
}
