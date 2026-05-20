// Spec: specs/024-captcha-registro-login/spec.md
//
// Tests de integración: el middleware CaptchaMiddleware montado sobre
// el handler de login verifica que:
//
//   - Sin captcha_token → 400 con mensaje en español.
//   - Con clave secreta de prueba de Cloudflare y token de prueba → 200/401
//     (según credenciales) — el captcha pasó, el handler continúa normal.
//
// Se usa un httptest.Server como mock de siteverify para no depender de
// la red real (AC-07). La clave de prueba oficial que siempre pasa es:
// secreto 1x0000000000000000000000000000000AA  (Cloudflare docs §Testing).
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"
)

// startMockSiteverify arranca un httptest.Server que simula el siteverify
// de Cloudflare respondiendo siempre success:true.
func startMockSiteverify(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mountLoginWithCaptcha monta el handler de login con el middleware de
// captcha apuntando al mock de siteverify.
func mountLoginWithCaptcha(db interface{ GetDB() interface{} }, mockURL string) *gin.Engine {
	panic("no usar directamente — ver mountLoginWithCaptchaDB")
}

// mountLoginWithCaptchaRouter crea el router de test con el middleware y
// el handler de login conectado a la base de datos dada.
func mountLoginWithCaptchaRouter(t *testing.T, mockSiteverifyURL string) *gin.Engine {
	t.Helper()
	db := setupLoginDB(t)

	// Inyectar un empleado para poder probar el flujo completo.
	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Test', 'test', datetime('now'))`,
		tenantID,
	).Error)
	pwd := "test-password-123"
	h, _ := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.MinCost)
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, 'Test', '3001234567', 'admin', ?, 1, 1, datetime('now'))`,
		uuid.NewString(), tenantID, string(h),
	).Error)

	svc := services.NewTurnstileService(
		"1x0000000000000000000000000000000AA", // clave de prueba oficial
		mockSiteverifyURL,
		&http.Client{Timeout: 5 * time.Second},
	)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login",
		middleware.CaptchaMiddleware(svc),
		handlers.Login(db, loginTestJWTSecret),
	)
	return r
}

// TestLoginCaptcha_SinToken verifica que el middleware rechaza el
// request cuando no se incluye captcha_token.
func TestLoginCaptcha_SinToken(t *testing.T) {
	mockSrv := startMockSiteverify(t)
	r := mountLoginWithCaptchaRouter(t, mockSrv.URL)

	body, _ := json.Marshal(map[string]string{
		"phone":    "3001234567",
		"password": "test-password-123",
		// sin captcha_token
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "verificación de seguridad requerida")
}

// TestLoginCaptcha_ConTokenDePrueba verifica que con el token de prueba
// de Cloudflare el middleware pasa y el handler de login continúa
// normalmente (200 con credenciales correctas).
func TestLoginCaptcha_ConTokenDePrueba(t *testing.T) {
	mockSrv := startMockSiteverify(t)
	r := mountLoginWithCaptchaRouter(t, mockSrv.URL)

	body, _ := json.Marshal(map[string]string{
		"phone":         "3001234567",
		"password":      "test-password-123",
		"captcha_token": "XXXX.DUMMY.TOKEN.XXXX", // cualquier token; el mock siempre pasa
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// El captcha pasó → el handler de login procesa la solicitud.
	// Con las credenciales correctas debe devolver 200.
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
