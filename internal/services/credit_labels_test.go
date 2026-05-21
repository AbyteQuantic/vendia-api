// Spec: specs/028-copy-fiar-credito-configurable/spec.md
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetCreditLabels_FiarMode verifies the complete label set for the
// default "fiar" mode (AC-01, AC-06 — backward-compatible).
func TestGetCreditLabels_FiarMode(t *testing.T) {
	l := GetCreditLabels("fiar")

	assert.Equal(t, "fiar", l.VerbInfinitive, "VerbInfinitive fiar")
	assert.Equal(t, "Fiar", l.VerbAction, "VerbAction fiar")
	assert.Equal(t, "Fiar", l.VerbActionShort, "VerbActionShort fiar")
	assert.Equal(t, "fiado", l.NounSingular, "NounSingular fiar")
	assert.Equal(t, "Fiado", l.NounSingularCapitalized, "NounSingularCapitalized fiar")
	assert.Equal(t, "fiados", l.NounPlural, "NounPlural fiar")
	assert.Equal(t, "Fiados", l.NounPluralCapitalized, "NounPluralCapitalized fiar")
	assert.Equal(t, "Cuaderno de fiados", l.CuadernoTitle, "CuadernoTitle fiar")
	assert.Equal(t, "Mis fiados", l.ScreenTitle, "ScreenTitle fiar")
	assert.Equal(t, "tiene un fiado abierto", l.CustomerHasOpenAccount, "CustomerHasOpenAccount fiar")
	assert.Equal(t, "Te recordamos tu fiado", l.WhatsAppReminderIntro, "WhatsAppReminderIntro fiar")
	assert.Equal(t, "Comprobante de fiado", l.ReceiptHeader, "ReceiptHeader fiar")
	// WhatsApp-specific labels
	assert.Equal(t, "ha fiado hoy", l.WhatsAppHandshakeVerb, "WhatsAppHandshakeVerb fiar")
	assert.Equal(t, "fiado pendiente de pago", l.WhatsAppReminderDebt, "WhatsAppReminderDebt fiar")
}

// TestGetCreditLabels_CreditMode verifies the complete label set for the
// "credit" mode (AC-02, AC-04, AC-05 — formal vocabulary for ferreteras/distribuidoras).
func TestGetCreditLabels_CreditMode(t *testing.T) {
	l := GetCreditLabels("credit")

	assert.Equal(t, "vender a crédito", l.VerbInfinitive, "VerbInfinitive credit")
	assert.Equal(t, "Vender a crédito", l.VerbAction, "VerbAction credit")
	assert.Equal(t, "A crédito", l.VerbActionShort, "VerbActionShort credit")
	assert.Equal(t, "venta a crédito", l.NounSingular, "NounSingular credit")
	assert.Equal(t, "Venta a crédito", l.NounSingularCapitalized, "NounSingularCapitalized credit")
	assert.Equal(t, "ventas a crédito", l.NounPlural, "NounPlural credit")
	assert.Equal(t, "Ventas a crédito", l.NounPluralCapitalized, "NounPluralCapitalized credit")
	assert.Equal(t, "Cuaderno de créditos", l.CuadernoTitle, "CuadernoTitle credit")
	assert.Equal(t, "Mis ventas a crédito", l.ScreenTitle, "ScreenTitle credit")
	assert.Equal(t, "tiene una venta a crédito abierta", l.CustomerHasOpenAccount, "CustomerHasOpenAccount credit")
	assert.Equal(t, "Te recordamos tu venta a crédito", l.WhatsAppReminderIntro, "WhatsAppReminderIntro credit")
	assert.Equal(t, "Comprobante de venta a crédito", l.ReceiptHeader, "ReceiptHeader credit")
	// WhatsApp-specific labels
	assert.Equal(t, "ha registrado hoy una venta a crédito de", l.WhatsAppHandshakeVerb, "WhatsAppHandshakeVerb credit")
	assert.Equal(t, "venta a crédito pendiente de pago", l.WhatsAppReminderDebt, "WhatsAppReminderDebt credit")
}

// TestGetCreditLabels_EmptyFallback verifies that an empty mode string
// falls back to the "fiar" vocabulary (defense in depth for legacy tenants
// whose DB row might not yet have the column populated — AC-06, FR-09).
func TestGetCreditLabels_EmptyFallback(t *testing.T) {
	l := GetCreditLabels("")
	assert.Equal(t, "fiado", l.NounSingular, "empty mode must fall back to fiar")
	assert.Equal(t, "Cuaderno de fiados", l.CuadernoTitle)
}

// TestGetCreditLabels_UnknownFallback verifies that an unrecognised mode
// string also falls back to "fiar" (defense in depth — DB constraint
// blocks invalid values, but Go code must be resilient).
func TestGetCreditLabels_UnknownFallback(t *testing.T) {
	l := GetCreditLabels("credito_libre")
	assert.Equal(t, "fiado", l.NounSingular, "unknown mode must fall back to fiar")
}
