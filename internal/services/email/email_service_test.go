// Spec: specs/042-modulo-eventos/spec.md
package email_test

import (
	"context"
	"strings"
	"testing"

	"vendia-backend/internal/services/email"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewService_NoConfig_UsesFakeSender(t *testing.T) {
	svc := email.NewService(email.Config{}) // no SMTP host
	assert.False(t, svc.IsConfigured(), "sin SMTP host el servicio degrada a FakeSender")
}

func TestService_SendQuotaReminder_BuildsSpanishBody(t *testing.T) {
	fake := &email.FakeSender{}
	svc := email.NewServiceWithSender(fake, "eventos@vendia.store")

	err := svc.SendQuotaReminder(context.Background(), email.QuotaReminder{
		To: "ana@example.com", Name: "Ana", EventTitle: "Curso de Repostería",
		AmountStr: "$50.000", DueDateStr: "15 de junio",
	})
	require.NoError(t, err)
	require.Len(t, fake.Sent, 1)

	msg := fake.Sent[0]
	assert.Equal(t, "ana@example.com", msg.To)
	body := strings.ToLower(msg.Subject + " " + msg.Body)
	for _, anchor := range []string{"ana", "curso de repostería", "$50.000", "15 de junio"} {
		assert.Contains(t, body, strings.ToLower(anchor))
	}
}

func TestService_SendEventReminder_BuildsSpanishBody(t *testing.T) {
	fake := &email.FakeSender{}
	svc := email.NewServiceWithSender(fake, "eventos@vendia.store")

	err := svc.SendEventReminder(context.Background(), email.EventReminder{
		To: "ana@example.com", Name: "Ana", EventTitle: "Hackatón VendIA", WhenStr: "mañana 9:00 a. m.",
	})
	require.NoError(t, err)
	require.Len(t, fake.Sent, 1)
	body := strings.ToLower(fake.Sent[0].Body)
	assert.Contains(t, body, "hackatón vendia")
	assert.Contains(t, body, "mañana")
}
