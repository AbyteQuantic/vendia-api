// Spec: specs/024-captcha-registro-login/spec.md
package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/services"
)

// mockSiteverify devuelve un handler HTTP que responde con la respuesta
// configurada en el campo success y el código HTTP dado.
func mockSiteverify(t *testing.T, statusCode int, success bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// El middleware debe enviar un POST con content-type form.
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(statusCode)
		resp := map[string]interface{}{"success": success}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestTurnstileService_VerifySuccess verifica que un token válido
// devuelve (true, nil).
func TestTurnstileService_VerifySuccess(t *testing.T) {
	srv := mockSiteverify(t, http.StatusOK, true)
	defer srv.Close()

	svc := services.NewTurnstileService("secret-key", srv.URL, &http.Client{Timeout: 5 * time.Second})
	ok, err := svc.Verify(context.Background(), "valid-token", "1.2.3.4")
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestTurnstileService_VerifySuccessFalse verifica que success:false
// devuelve (false, nil) — token inválido o expirado.
func TestTurnstileService_VerifySuccessFalse(t *testing.T) {
	srv := mockSiteverify(t, http.StatusOK, false)
	defer srv.Close()

	svc := services.NewTurnstileService("secret-key", srv.URL, &http.Client{Timeout: 5 * time.Second})
	ok, err := svc.Verify(context.Background(), "bad-token", "1.2.3.4")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestTurnstileService_VerifyEmptyToken verifica que un token vacío
// devuelve error sin llegar a llamar a Cloudflare.
func TestTurnstileService_VerifyEmptyToken(t *testing.T) {
	// El servidor no debería ser llamado, pero lo creamos para detectar
	// si la implementación lo llama de todos modos.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))
	defer srv.Close()

	svc := services.NewTurnstileService("secret-key", srv.URL, &http.Client{Timeout: 5 * time.Second})
	ok, err := svc.Verify(context.Background(), "", "1.2.3.4")
	assert.Error(t, err)
	assert.False(t, ok)
	assert.False(t, called, "no debe llamar a Cloudflare con token vacío")
}

// TestTurnstileService_VerifyTimeout verifica que si el servidor tarda
// más que el timeout del cliente se devuelve error.
func TestTurnstileService_VerifyTimeout(t *testing.T) {
	// Usamos un contexto cancelable para desbloquear el handler cuando el
	// test termina, así srv.Close() no se cuelga esperando la conexión.
	handlerCtx, cancelHandler := context.WithCancel(context.Background())
	t.Cleanup(cancelHandler)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-handlerCtx.Done()
	}))

	// Cliente con timeout de 50ms para que el test no sea lento.
	svc := services.NewTurnstileService("secret-key", srv.URL, &http.Client{Timeout: 50 * time.Millisecond})
	ok, err := svc.Verify(context.Background(), "some-token", "1.2.3.4")
	assert.Error(t, err)
	assert.False(t, ok)

	// Cancelar el contexto para que el handler salga y srv.Close() no cuelgue.
	cancelHandler()
	srv.Close()
}

// TestTurnstileService_Verify5xxUpstream verifica que un 5xx de
// Cloudflare devuelve error.
func TestTurnstileService_Verify5xxUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := services.NewTurnstileService("secret-key", srv.URL, &http.Client{Timeout: 5 * time.Second})
	ok, err := svc.Verify(context.Background(), "some-token", "1.2.3.4")
	assert.Error(t, err)
	assert.False(t, ok)
}
