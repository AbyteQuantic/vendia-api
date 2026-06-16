// Spec: specs/057-panic-button-delivery/spec.md
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeCoPhone(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"3001234567", "573001234567"},
		{"(300) 123-4567", "573001234567"},
		{"+57 300 123 4567", "573001234567"},
		{"6011234567", "6011234567"}, // fijo, no celular → sin 57
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, NormalizeCoPhone(tt.in), "in=%q", tt.in)
	}
}

func TestNotifier_FailClosed_WhenUnconfigured(t *testing.T) {
	// Sin env vars, ambos canales están "no configurados".
	t.Setenv("TWILIO_ACCOUNT_SID", "")
	t.Setenv("TWILIO_AUTH_TOKEN", "")
	t.Setenv("TWILIO_FROM_NUMBER", "")
	t.Setenv("META_WA_PHONE_ID", "")
	t.Setenv("META_WA_TOKEN", "")
	t.Setenv("META_WA_TEMPLATE", "")

	n := NewEmergencyNotifier()
	assert.False(t, n.SMSConfigured())
	assert.False(t, n.WhatsAppConfigured())

	// Dispatch NO debe intentar enviar ni mentir: devuelve skipped.
	sms := n.Dispatch("sms", "3001234567", "EMERGENCIA")
	assert.Equal(t, DeliverySkipped, sms.Status)
	assert.NotEmpty(t, sms.Error)

	wa := n.Dispatch("whatsapp", "3001234567", "EMERGENCIA")
	assert.Equal(t, DeliverySkipped, wa.Status)

	// Método desconocido cae a WhatsApp (default histórico).
	def := n.Dispatch("", "3001234567", "EMERGENCIA")
	assert.Equal(t, DeliverySkipped, def.Status)
}

func TestSanitizeTemplateParam_NoNewlines(t *testing.T) {
	// El mensaje real de pánico trae \n\n (dirección) + URL de Maps.
	in := "EMERGENCIA en el local. Necesito ayuda.\n\nDireccion: Cra 5 #12-34\n\nUbicacion: https://maps.google.com/?q=4.6,-74.0"
	out := sanitizeTemplateParam(in)
	assert.NotContains(t, out, "\n", "Meta rechaza saltos de línea en params de plantilla")
	assert.NotContains(t, out, "\t")
	// No deja runs de 4+ espacios.
	assert.NotContains(t, out, "    ")
	// Conserva el contenido esencial.
	assert.Contains(t, out, "EMERGENCIA")
	assert.Contains(t, out, "maps.google.com")
}

func TestNotifier_Configured_Flags(t *testing.T) {
	t.Setenv("TWILIO_ACCOUNT_SID", "AC123")
	t.Setenv("TWILIO_AUTH_TOKEN", "tok")
	t.Setenv("TWILIO_FROM_NUMBER", "+15550001111")
	t.Setenv("META_WA_PHONE_ID", "")
	t.Setenv("META_WA_TOKEN", "")
	t.Setenv("META_WA_TEMPLATE", "")

	n := NewEmergencyNotifier()
	assert.True(t, n.SMSConfigured())
	assert.False(t, n.WhatsAppConfigured())
}
