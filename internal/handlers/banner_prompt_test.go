package handlers

import (
	"strings"
	"testing"
)

// The V2 banner prompt is THE product differentiator of the Marketing
// Hub — if any of the commercial strings silently stops reaching
// Gemini, banners regress to "menu photos" again. These tests pin
// down that every dollar figure, brand, % OFF label, and savings
// string is preserved verbatim in the assembled prompt.

func TestBuildPromoBannerPrompt_V2_InjectsEveryFinancialString(t *testing.T) {
	got := BuildPromoBannerPrompt(PromoBannerPromptInput{
		TenantName:     "Don Brayan",
		ComboTitle:     "Empanada & Coca-Cola",
		Products:       []string{"Empanada de pollo", "Coca-Cola 350ml"},
		Tone:           "Vibrante",
		NormalPriceStr: "$9.500",
		PromoPriceStr:  "$8.100",
		DiscountStr:    "14% OFF",
		SavingsStr:     "Ahorras $1.400",
		// Legacy fields blank — V2 branch must still work.
	})

	mustContain := []string{
		"Don Brayan",
		"Empanada & Coca-Cola",
		"Empanada de pollo, Coca-Cola 350ml",
		"Vibrante",
		`"$9.500"`,   // precio normal, tachado
		`"$8.100"`,   // precio promo, héroe
		`"14% OFF"`,  // sello de descuento
		`"Ahorras $1.400"`,
		"JERARQUÍA TIPOGRÁFICA OBLIGATORIA",
		"PRECIO PROMO",
		"PRECIO NORMAL TACHADO",
		"ANTI-PATRONES EXPLÍCITOS",
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("V2 prompt missing required string %q", s)
		}
	}

	// Anti-pattern guard: the banner must never instruct Gemini to
	// use the $X,XXX.XX USD format. The prompt explicitly forbids it.
	if !strings.Contains(got, "$8,100.00") {
		// "$8,100.00" only appears as the BAD example; if it's not
		// quoted in the anti-pattern list we lost the protection.
		t.Errorf("V2 prompt must include the forbidden USD format literal as anti-pattern")
	}
}

func TestBuildPromoBannerPrompt_V2_SkipsBlankFieldsFromHierarchy(t *testing.T) {
	// Caller only knew precio promo and % OFF. We must NOT print a
	// "PRECIO NORMAL TACHADO — TEXTO LITERAL: \"\"" line — that would
	// instruct Gemini to render empty text.
	got := BuildPromoBannerPrompt(PromoBannerPromptInput{
		PromoName:     "Promo relámpago",
		Products:      []string{"Galleta Festival"},
		PromoPriceStr: "$2.500",
		DiscountStr:   "20% OFF",
		Tone:          "urgente",
	})
	if strings.Contains(got, `TEXTO LITERAL: ""`) {
		t.Errorf("blank fields must be dropped from the hierarchy, got:\n%s", got)
	}
	if !strings.Contains(got, `"$2.500"`) {
		t.Errorf("expected promo price in prompt, got:\n%s", got)
	}
}

func TestBuildPromoBannerPrompt_FallsBackToV1WhenFinancialsMissing(t *testing.T) {
	// Cliente Flutter viejo: no envía precios V2. El prompt debe caer
	// al formato V1 sin crashear y sin perder los inputs que sí llegaron.
	got := BuildPromoBannerPrompt(PromoBannerPromptInput{
		PromoName:    "2x1 en Pan",
		Products:     []string{"Pan francés"},
		DiscountText: "2x1",
		Tone:         "vibrante",
	})
	if !strings.Contains(got, "PROMOCIÓN: 2x1 en Pan") {
		t.Errorf("V1 fallback lost promo name. Got:\n%s", got)
	}
	if !strings.Contains(got, "TEXTO DE DESCUENTO PRINCIPAL: 2x1") {
		t.Errorf("V1 fallback lost discount text. Got:\n%s", got)
	}
	// V2-only markers should be absent so we know the branch was taken.
	if strings.Contains(got, "JERARQUÍA TIPOGRÁFICA OBLIGATORIA") {
		t.Errorf("expected V1 prompt, but V2 markers leaked in:\n%s", got)
	}
}

func TestBuildPromoBannerPrompt_ToneDefaultsToVibrante(t *testing.T) {
	got := BuildPromoBannerPrompt(PromoBannerPromptInput{
		PromoName:      "x",
		Products:       []string{"y"},
		PromoPriceStr:  "$1.000",
		NormalPriceStr: "$2.000",
		Tone:           "   ", // whitespace only
	})
	if !strings.Contains(got, `Tono visual:        vibrante`) {
		t.Errorf("expected fallback to 'vibrante' for blank tone, got:\n%s", got)
	}
}

func TestBuildPromoBannerPrompt_NeverInventsPrices(t *testing.T) {
	// Regresión: si el caller pasa productos pero cero info financiera,
	// el prompt V1 NO debe mencionar precios específicos. El modelo no
	// puede inventarlos — mejor que el banner salga sin números a que
	// salgan precios incorrectos.
	got := BuildPromoBannerPrompt(PromoBannerPromptInput{
		PromoName: "Oferta sorpresa",
		Products:  []string{"Arroz 1kg"},
	})
	for _, forbidden := range []string{"$100", "$1.000", "50% OFF"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("prompt invented %q without caller input:\n%s", forbidden, got)
		}
	}
}
