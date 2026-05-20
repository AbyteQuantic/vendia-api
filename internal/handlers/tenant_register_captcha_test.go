// Spec: specs/024-captcha-registro-login/spec.md
//
// Tests de integración: el middleware CaptchaMiddleware montado sobre
// el handler de registro verifica que:
//
//   - Sin captcha_token → 400 con mensaje en español.
//   - Con token válido y base de datos disponible → el handler continúa (201/4xx
//     según los datos del body).
//
// Los tests que necesitan PostgreSQL hacen skip gracefully si el Docker
// no está corriendo (igual que el resto de tests de tenant_register_test.go).
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/config"
	"vendia-backend/internal/database"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"
)

// mountRegisterWithCaptchaRouter crea un router de test con el middleware
// de captcha apuntando al mock de siteverify y el handler de registro.
// Si PostgreSQL no está disponible, el test se salta.
func mountRegisterWithCaptchaRouter(t *testing.T, mockSiteverifyURL string) *gin.Engine {
	t.Helper()

	// Skip gracefully si no hay Docker PostgreSQL.
	conn, err := net.DialTimeout("tcp", "localhost:5499", 1*time.Second)
	if err != nil {
		t.Skip("Skipping: Docker PostgreSQL no disponible (ejecutar 'make local')")
	}
	conn.Close()

	cfg := &config.Config{
		DatabaseURL: "postgres://vendia:vendia_secret@localhost:5499/vendia?sslmode=disable",
		JWTSecret:   testSecret,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Skip("Skipping: Docker PostgreSQL no disponible")
	}
	require.NoError(t, database.Migrate(db))

	svc := services.NewTurnstileService(
		"1x0000000000000000000000000000000AA", // clave de prueba oficial
		mockSiteverifyURL,
		&http.Client{Timeout: 5 * time.Second},
	)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/tenant/register",
		middleware.CaptchaMiddleware(svc),
		handlers.TenantRegister(db, testSecret),
	)
	return r
}

// mountRegisterWithCaptchaNoDBRouter crea un router con captcha pero sin
// base de datos real — sólo sirve para verificar que el middleware rechaza
// antes de que llegue al handler.
func mountRegisterWithCaptchaNoDBRouter(t *testing.T, mockSiteverifyURL string) *gin.Engine {
	t.Helper()

	svc := services.NewTurnstileService(
		"1x0000000000000000000000000000000AA",
		mockSiteverifyURL,
		&http.Client{Timeout: 5 * time.Second},
	)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Handler dummy: si el middleware pasa, el handler responde 200.
	// El test verifica que sin token el middleware lo rechaza antes.
	r.POST("/api/v1/tenant/register",
		middleware.CaptchaMiddleware(svc),
		func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		},
	)
	return r
}

// TestRegisterCaptcha_SinToken_NoRequiereDB verifica que el middleware
// rechaza el request sin captcha_token sin necesidad de base de datos.
func TestRegisterCaptcha_SinToken_NoRequiereDB(t *testing.T) {
	mockSrv := startMockSiteverify(t)
	r := mountRegisterWithCaptchaNoDBRouter(t, mockSrv.URL)

	body, _ := json.Marshal(map[string]interface{}{
		"owner": map[string]string{
			"name":     "Don Brayan",
			"phone":    "3001234567",
			"password": "supersecreto",
		},
		"business": map[string]string{
			"name": "La Tiendita",
			"type": "tienda_barrio",
		},
		// sin captcha_token
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/tenant/register",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "verificación de seguridad requerida")
}

// TestRegisterCaptcha_ConTokenDePrueba requiere PostgreSQL Docker.
// Verifica que con token válido el handler de registro continúa
// normalmente (el captcha pasó).
func TestRegisterCaptcha_ConTokenDePrueba(t *testing.T) {
	mockSrv := startMockSiteverify(t)
	r := mountRegisterWithCaptchaRouter(t, mockSrv.URL)

	phone := uniquePhone()
	payload := map[string]interface{}{
		"captcha_token": "XXXX.DUMMY.TOKEN.XXXX",
		"owner": map[string]string{
			"name":     "Don Test",
			"phone":    phone,
			"password": "supersecreto123",
		},
		"business": map[string]string{
			"name": "La Tiendita Test",
			"type": "tienda_barrio",
		},
	}

	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/tenant/register",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	// El captcha pasó → el handler de registro continúa.
	// Esperamos 201 con un registro nuevo.
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}
