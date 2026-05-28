// Spec: specs/038-push-notifications-web-android/spec.md
package push

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T-05a-1 — `FakeSender.Send` con lista de tokens vacía es no-op,
// no falla, no captura una llamada. Defensa contra el caso "tenant
// sin tokens registrados".
func TestFakeSender_NoTokensIsNoop(t *testing.T) {
	fs := &FakeSender{}
	result, err := fs.Send(context.Background(), nil, Payload{
		Title: "x",
		Body:  "y",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Sent)
	assert.Empty(t, result.Invalid)
	assert.Empty(t, fs.Calls, "no debe registrar la llamada — no había trabajo que hacer")
}

// T-05a-2 — `FakeSender.Send` con tokens captura la llamada (tokens,
// payload) para que los tests del dispatcher puedan assertear.
func TestFakeSender_CapturesCall(t *testing.T) {
	fs := &FakeSender{}
	payload := Payload{Title: "Pedido nuevo", Body: "Pedro pidió 2 unidades", DeepLink: "/pedidos/abc"}
	result, err := fs.Send(context.Background(), []string{"tok-a", "tok-b"}, payload)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Sent)
	assert.Empty(t, result.Invalid)

	require.Len(t, fs.Calls, 1)
	call := fs.Calls[0]
	assert.Equal(t, []string{"tok-a", "tok-b"}, call.Tokens)
	assert.Equal(t, payload, call.Payload)
}

// T-05a-3 — `FakeSender` permite simular tokens reportados inválidos
// por FCM en producción (caso `IsRegistrationTokenNotRegistered`). El
// dispatcher consume `result.Invalid` para marcar los rows con
// `invalidated_at`. AC-10.
func TestFakeSender_SimulatesInvalidTokens(t *testing.T) {
	fs := &FakeSender{InvalidateTokens: map[string]bool{"tok-bad": true}}
	result, err := fs.Send(context.Background(), []string{"tok-good", "tok-bad", "tok-also-good"}, Payload{Title: "x"})

	require.NoError(t, err)
	assert.Equal(t, 2, result.Sent)
	assert.ElementsMatch(t, []string{"tok-bad"}, result.Invalid)
}

// T-05a-4 — `FakeSender` permite simular un error transitorio (red,
// FCM 5xx) — el dispatcher debe registrar el error y NO setear
// `pushed_at` en la notificación. El test del dispatcher consumirá
// esto; acá solo verificamos que el fake propaga el error.
func TestFakeSender_PropagatesError(t *testing.T) {
	wantErr := errors.New("simulated FCM 503")
	fs := &FakeSender{NextError: wantErr}
	_, err := fs.Send(context.Background(), []string{"tok-x"}, Payload{Title: "x"})
	require.ErrorIs(t, err, wantErr)
	// Un error transitorio no consume el "queue" — el siguiente Send
	// no debe seguir fallando.
	_, err = fs.Send(context.Background(), []string{"tok-x"}, Payload{Title: "x"})
	require.NoError(t, err)
}

// T-05a-5 — `Payload` lleva DeepLink opcional. Si no se setea, el
// fake lo refleja vacío sin romper.
func TestFakeSender_PayloadWithoutDeepLink(t *testing.T) {
	fs := &FakeSender{}
	_, err := fs.Send(context.Background(), []string{"tok"}, Payload{Title: "x", Body: "y"})
	require.NoError(t, err)
	require.Len(t, fs.Calls, 1)
	assert.Empty(t, fs.Calls[0].Payload.DeepLink)
}

// T-05a-6 — La interfaz pública del paquete expone solamente lo que el
// resto del backend necesita (Sender, Payload, SendResult). El
// FCMSender concreto se construye con NewFCMSender(); error de init
// es retornado al caller — un nil sender no debe colarse a runtime.
func TestNewFCMSender_RejectsEmptyCredentials(t *testing.T) {
	_, err := NewFCMSender(context.Background(), FCMConfig{
		ServiceAccountJSON: "",
		ProjectID:          "",
	})
	require.Error(t, err)
}
