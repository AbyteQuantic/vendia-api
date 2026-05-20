// Spec: specs/024-captcha-registro-login/spec.md
//
// CaptchaMiddleware valida tokens de Cloudflare Turnstile en cada
// request. El middleware:
//
//  1. Lee el body completo (io.ReadAll) y lo restituye (io.NopCloser)
//     para que el handler downstream pueda parsearlo sin perder bytes.
//  2. Extrae captcha_token del JSON usando un struct mínimo.
//  3. Si el token está vacío → 400 "verificación de seguridad requerida".
//  4. Llama al servicio Turnstile (inyectable) con el token y la IP.
//  5. Si falla → 400 "verificación de seguridad falló, intente de nuevo".
//  6. Si OK → c.Next().
//
// La activación es opt-in: el middleware solo se registra en main.go
// cuando TURNSTILE_ENABLED=true (FR-08, AC-09, D4).
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// TurnstileVerifier es la interfaz que debe implementar el servicio de
// validación. Permite inyectar mocks en tests sin depender de la red.
type TurnstileVerifier interface {
	Verify(ctx context.Context, token, remoteIP string) (bool, error)
}

// captchaTokenExtract es el struct mínimo usado para extraer solo
// captcha_token del body JSON sin deserializar el payload completo.
type captchaTokenExtract struct {
	CaptchaToken string `json:"captcha_token"`
}

// CaptchaMiddleware retorna un gin.HandlerFunc que valida el token
// Cloudflare Turnstile del body JSON de cada request. El servicio
// inyectado (svc) hace la validación real contra Cloudflare (o el mock
// en tests).
func CaptchaMiddleware(svc TurnstileVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Leer el body completo para poder extraer el token y luego
		//    restituirlo al stream para que el handler downstream no lo
		//    encuentre vacío. Body nil es tratado como body vacío.
		var body []byte
		if c.Request.Body != nil {
			var err error
			body, err = io.ReadAll(c.Request.Body)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"error": "verificación de seguridad requerida",
				})
				return
			}
		}
		// Restituir el body al request (siempre, incluso si estaba vacío).
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

		// 2. Extraer captcha_token del JSON.
		var extract captchaTokenExtract
		// Toleramos que json.Unmarshal falle (body no-JSON, body vacío)
		// — en ese caso CaptchaToken quedará vacío y rechazaremos igual.
		_ = json.Unmarshal(body, &extract)

		if extract.CaptchaToken == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "verificación de seguridad requerida",
			})
			return
		}

		// 3. Validar el token con el servicio Turnstile.
		ok, err := svc.Verify(c.Request.Context(), extract.CaptchaToken, c.ClientIP())
		if err != nil || !ok {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "verificación de seguridad falló, intente de nuevo",
			})
			return
		}

		c.Next()
	}
}
