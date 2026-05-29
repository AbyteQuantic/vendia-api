// Spec: specs/038-push-notifications-web-android/spec.md
package push

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fcmTargets(ids ...string) []Target {
	out := make([]Target, len(ids))
	for i, id := range ids {
		out[i] = Target{DeviceID: id, FCMToken: "fcm-" + id}
	}
	return out
}

// T-05a-1 — `FakeSender.Send` con targets vacíos es no-op.
func TestFakeSender_NoTargetsIsNoop(t *testing.T) {
	fs := &FakeSender{}
	result, err := fs.Send(context.Background(), nil, Payload{Title: "x", Body: "y"})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Sent)
	assert.Empty(t, result.Invalid)
	assert.Empty(t, fs.Calls)
}

// T-05a-2 — Captura targets y payload.
func TestFakeSender_CapturesCall(t *testing.T) {
	fs := &FakeSender{}
	payload := Payload{Title: "Pedido nuevo", Body: "Pedro pidió 2", DeepLink: "/pedidos/abc"}
	targets := fcmTargets("d1", "d2")
	result, err := fs.Send(context.Background(), targets, payload)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Sent)
	assert.Empty(t, result.Invalid)

	require.Len(t, fs.Calls, 1)
	assert.Equal(t, targets, fs.Calls[0].Targets)
	assert.Equal(t, payload, fs.Calls[0].Payload)
}

// T-05a-3 — Simula device IDs reportados inválidos por el proveedor.
func TestFakeSender_SimulatesInvalidDevices(t *testing.T) {
	fs := &FakeSender{InvalidateDeviceIDs: map[string]bool{"d2": true}}
	result, err := fs.Send(context.Background(), fcmTargets("d1", "d2", "d3"), Payload{Title: "x"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Sent)
	assert.ElementsMatch(t, []string{"d2"}, result.Invalid)
}

// T-05a-4 — Error transitorio se propaga y NO se queda pegado.
func TestFakeSender_PropagatesError(t *testing.T) {
	wantErr := errors.New("simulated 503")
	fs := &FakeSender{NextError: wantErr}
	_, err := fs.Send(context.Background(), fcmTargets("d1"), Payload{Title: "x"})
	require.ErrorIs(t, err, wantErr)
	_, err = fs.Send(context.Background(), fcmTargets("d1"), Payload{Title: "x"})
	require.NoError(t, err)
}

// T-05a-5 — Payload sin DeepLink se refleja vacío sin romper.
func TestFakeSender_PayloadWithoutDeepLink(t *testing.T) {
	fs := &FakeSender{}
	_, err := fs.Send(context.Background(), fcmTargets("d1"), Payload{Title: "x", Body: "y"})
	require.NoError(t, err)
	require.Len(t, fs.Calls, 1)
	assert.Empty(t, fs.Calls[0].Payload.DeepLink)
}

// T-05a-6 — UnifiedSender falla si AMBOS backends están vacíos.
func TestNewUnifiedSender_RejectsBothEmpty(t *testing.T) {
	_, err := NewUnifiedSender(context.Background(),
		FCMConfig{}, VAPIDConfig{})
	require.Error(t, err)
}

// T-05a-7 — Targets Web Push (sin FCMToken) son aceptados por el
// FakeSender. Es el caso iOS Safari.
func TestFakeSender_AcceptsWebPushTargets(t *testing.T) {
	fs := &FakeSender{}
	targets := []Target{{
		DeviceID: "d-ios",
		Endpoint: "https://web.push.apple.com/abc",
		P256dh:   "pubkey",
		Auth:     "authsecret",
	}}
	result, err := fs.Send(context.Background(), targets, Payload{Title: "x"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Sent)
}
