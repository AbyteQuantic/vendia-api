// Spec: specs/024-captcha-registro-login/spec.md
package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/middleware"
)

// mockTurnstileService implementa middleware.TurnstileVerifier para tests.
type mockTurnstileService struct {
	// ok es el valor que retorna Verify.
	ok bool
	// err es el error que retorna Verify (si no es nil, ok se ignora).
	err error
}

func (m *mockTurnstileService) Verify(_ context.Context, _, _ string) (bool, error) {
	return m.ok, m.err
}

// setupCaptchaRouter crea un router de prueba con el middleware montado
// y un handler downstream que lee y retorna el body para verificar que
// no fue consumido por el middleware.
func setupCaptchaRouter(svc middleware.TurnstileVerifier) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/test", middleware.CaptchaMiddleware(svc), func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"body": string(body)})
	})
	return r
}

// TestCaptchaMiddleware_SinBody verifica que un request sin body
// devuelve 400 con el mensaje de verificación requerida.
func TestCaptchaMiddleware_SinBody(t *testing.T) {
	r := setupCaptchaRouter(&mockTurnstileService{ok: true})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "verificación de seguridad requerida")
}

// TestCaptchaMiddleware_SinCaptchaToken verifica que un body sin
// captcha_token devuelve 400 con el mensaje de verificación requerida.
func TestCaptchaMiddleware_SinCaptchaToken(t *testing.T) {
	r := setupCaptchaRouter(&mockTurnstileService{ok: true})

	body := `{"phone": "3001234567", "password": "secret"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "verificación de seguridad requerida")
}

// TestCaptchaMiddleware_TokenInvalido verifica que cuando el servicio
// retorna (false, nil), el middleware responde 400 con el mensaje de
// verificación fallida.
func TestCaptchaMiddleware_TokenInvalido(t *testing.T) {
	r := setupCaptchaRouter(&mockTurnstileService{ok: false, err: nil})

	body := `{"captcha_token": "bad-token", "phone": "3001234567"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "verificación de seguridad falló, intente de nuevo")
}

// TestCaptchaMiddleware_ErrorServicio verifica que cuando el servicio
// retorna un error (p.ej. timeout), el middleware responde 400.
func TestCaptchaMiddleware_ErrorServicio(t *testing.T) {
	r := setupCaptchaRouter(&mockTurnstileService{ok: false, err: errors.New("timeout")})

	body := `{"captcha_token": "some-token", "phone": "3001234567"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "verificación de seguridad falló, intente de nuevo")
}

// TestCaptchaMiddleware_TokenValido verifica que con un token válido
// el handler downstream es invocado (c.Next()) y el body está íntegro
// — el middleware no lo consumió.
func TestCaptchaMiddleware_TokenValido(t *testing.T) {
	r := setupCaptchaRouter(&mockTurnstileService{ok: true})

	originalBody := `{"captcha_token": "valid-token", "phone": "3001234567", "password": "pass"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/test", strings.NewReader(originalBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// El handler downstream devuelve {"body": "<json escapeado>"}.
	// Comprobamos que el campo body contiene los datos originales.
	// El JSON usa escape de comillas (\"), así que buscamos las claves sin comillas.
	assert.Contains(t, w.Body.String(), `phone`)
	assert.Contains(t, w.Body.String(), `captcha_token`)
}

// TestCaptchaMiddleware_BodyRestituido verifica explícitamente que el
// body completo fue restituido al handler downstream intacto.
func TestCaptchaMiddleware_BodyRestituido(t *testing.T) {
	r := setupCaptchaRouter(&mockTurnstileService{ok: true})

	originalBody := `{"captcha_token":"tok","phone":"300","password":"abc123"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/test",
		bytes.NewBufferString(originalBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// El handler downstream retorna {"body": "<json escapeado>"}.
	// Verificamos que los valores llegaron íntegros.
	assert.Contains(t, w.Body.String(), `phone`)
	assert.Contains(t, w.Body.String(), `300`)
	assert.Contains(t, w.Body.String(), `abc123`)
}
