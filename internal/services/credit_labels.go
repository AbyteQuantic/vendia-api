// Spec: specs/028-copy-fiar-credito-configurable/spec.md
package services

// CreditLabels holds every user-facing string that varies between the
// "fiar" and "credit" vocabulary modes (Spec F028 §8).
//
// Internal identifiers (fiado_token, /fiado/<token>, enable_fiados,
// CreditAccount table name, etc.) are intentionally excluded — they do
// NOT change with the mode (FR-11).
type CreditLabels struct {
	// VerbInfinitive: "fiar" | "vender a crédito"
	VerbInfinitive string
	// VerbAction: "Fiar" | "Vender a crédito"
	VerbAction string
	// VerbActionShort: "Fiar" | "A crédito"
	VerbActionShort string
	// NounSingular: "fiado" | "venta a crédito"
	NounSingular string
	// NounSingularCapitalized: "Fiado" | "Venta a crédito"
	NounSingularCapitalized string
	// NounPlural: "fiados" | "ventas a crédito"
	NounPlural string
	// NounPluralCapitalized: "Fiados" | "Ventas a crédito"
	NounPluralCapitalized string
	// CuadernoTitle: "Cuaderno de fiados" | "Cuaderno de créditos"
	CuadernoTitle string
	// ScreenTitle: "Mis fiados" | "Mis ventas a crédito"
	ScreenTitle string
	// CustomerHasOpenAccount: "tiene un fiado abierto" | "tiene una venta a crédito abierta"
	CustomerHasOpenAccount string
	// WhatsAppReminderIntro: "Te recordamos tu fiado" | "Te recordamos tu venta a crédito"
	WhatsAppReminderIntro string
	// ReceiptHeader: "Comprobante de fiado" | "Comprobante de venta a crédito"
	ReceiptHeader string
	// WhatsAppHandshakeVerb — embedded verb phrase used in CreditHandshake messages:
	// "ha fiado hoy" | "ha registrado hoy una venta a crédito de"
	WhatsAppHandshakeVerb string
	// WhatsAppReminderDebt — noun phrase used in CreditReminder messages:
	// "fiado pendiente de pago" | "venta a crédito pendiente de pago"
	WhatsAppReminderDebt string
}

var labelsFiar = CreditLabels{
	VerbInfinitive:          "fiar",
	VerbAction:              "Fiar",
	VerbActionShort:         "Fiar",
	NounSingular:            "fiado",
	NounSingularCapitalized: "Fiado",
	NounPlural:              "fiados",
	NounPluralCapitalized:   "Fiados",
	CuadernoTitle:           "Cuaderno de fiados",
	ScreenTitle:             "Mis fiados",
	CustomerHasOpenAccount:  "tiene un fiado abierto",
	WhatsAppReminderIntro:   "Te recordamos tu fiado",
	ReceiptHeader:           "Comprobante de fiado",
	WhatsAppHandshakeVerb:   "ha fiado hoy",
	WhatsAppReminderDebt:    "fiado pendiente de pago",
}

var labelsCredit = CreditLabels{
	VerbInfinitive:          "vender a crédito",
	VerbAction:              "Vender a crédito",
	VerbActionShort:         "A crédito",
	NounSingular:            "venta a crédito",
	NounSingularCapitalized: "Venta a crédito",
	NounPlural:              "ventas a crédito",
	NounPluralCapitalized:   "Ventas a crédito",
	CuadernoTitle:           "Cuaderno de créditos",
	ScreenTitle:             "Mis ventas a crédito",
	CustomerHasOpenAccount:  "tiene una venta a crédito abierta",
	WhatsAppReminderIntro:   "Te recordamos tu venta a crédito",
	ReceiptHeader:           "Comprobante de venta a crédito",
	WhatsAppHandshakeVerb:   "ha registrado hoy una venta a crédito de",
	WhatsAppReminderDebt:    "venta a crédito pendiente de pago",
}

// GetCreditLabels returns the CreditLabels for the given mode.
// Falls back to "fiar" if mode is empty or unrecognised — defense in depth
// so legacy tenants without the column populated behave exactly as before
// F028 (FR-09, AC-06).
func GetCreditLabels(mode string) CreditLabels {
	if mode == "credit" {
		return labelsCredit
	}
	return labelsFiar
}
