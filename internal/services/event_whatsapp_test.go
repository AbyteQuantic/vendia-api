// Spec: specs/042-modulo-eventos/spec.md (§12 D1)
package services_test

import (
	"strings"
	"testing"

	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
)

func TestEventReminderMessage_IncludesEventAndWhen(t *testing.T) {
	wa := services.NewWhatsAppService()
	msg := wa.EventReminderMessage("Ana", "Tienda Doña Ana", "Hackatón VendIA", "mañana 9:00 a. m.")
	low := strings.ToLower(msg)
	for _, anchor := range []string{"ana", "hackatón vendia", "tienda doña ana", "mañana"} {
		assert.Contains(t, low, anchor)
	}
}

func TestEventQuotaReminderMessage_IncludesAmountAndDue(t *testing.T) {
	wa := services.NewWhatsAppService()
	msg := wa.EventQuotaReminderMessage("Ana", "Tienda Doña Ana", "Diplomado", 50000, "15/06/2026")
	low := strings.ToLower(msg)
	assert.Contains(t, low, "diplomado")
	assert.Contains(t, low, "50.000") // formatCOP groups thousands
	assert.Contains(t, low, "15/06/2026")
}

// A reminder built into a wa.me deep-link must be URL-launchable.
func TestEventReminder_BuildsWaMeURL(t *testing.T) {
	wa := services.NewWhatsAppService()
	msg := wa.EventReminderMessage("Ana", "Tienda", "Curso", "mañana")
	url := wa.BuildURL("573001234567", msg)
	assert.True(t, strings.HasPrefix(url, "https://wa.me/573001234567?text="))
}
