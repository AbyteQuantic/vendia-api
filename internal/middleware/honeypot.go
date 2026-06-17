// Spec: specs/064-anti-bot-honeypot/spec.md
//
// HoneypotMiddleware es la capa anti-bot que reemplaza a Cloudflare Turnstile
// (ver specs/024 y 025). A diferencia del captcha, NO hace ninguna llamada a un
// servidor externo, así que es imposible que se cuelgue en redes móviles
// (la causa por la que se apagó Turnstile el 2026-05-20).
//
// Estrategia:
//
//  1. Lee el body completo y lo restituye (io.NopCloser) para que el handler
//     downstream lo parsee sin perder bytes — mismo patrón que CaptchaMiddleware.
//  2. Extrae el campo trampa `website` y el opcional `form_elapsed_ms`.
//  3. Si `website` llega NO vacío → el cliente es un bot que auto-llenó un input
//     oculto → 400 con mensaje neutro (no revela que es honeypot).
//  4. Si `form_elapsed_ms` viaja y es absurdamente pequeño (< minFillMs) → bot
//     que envió al instante → 400. Umbral ultra-bajo para JAMÁS bloquear a un
//     humano (aprendizaje del incidente de Turnstile).
//  5. Si OK → c.Next().
//
// Es always-on: no depende de ninguna env var. Para humanos es invisible.
package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// minFillMs es el tiempo mínimo (ms) plausible entre que un humano abre el
// formulario y lo envía. 800 ms es físicamente imposible de cumplir revisando
// un carrito; solo un bot que envía al instante cae por debajo. Deliberadamente
// conservador para no reintroducir falsos positivos.
const minFillMs = 800

// honeypotExtract es el struct mínimo para leer solo los campos anti-bot del
// body sin deserializar el payload completo.
type honeypotExtract struct {
	// Website es el campo trampa: oculto en el formulario, los humanos nunca
	// lo ven ni lo llenan; los bots que auto-llenan inputs sí.
	Website string `json:"website"`
	// FormElapsedMs es opcional (puntero): nil cuando el cliente no lo envía
	// (app Flutter, clientes viejos) → no se aplica el chequeo de tiempo.
	FormElapsedMs *int64 `json:"form_elapsed_ms"`
}

// HoneypotMiddleware retorna un gin.HandlerFunc que rechaza requests con señales
// de bot (campo trampa lleno o envío instantáneo) sin ninguna llamada externa.
func HoneypotMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Leer y restituir el body (body nil = body vacío).
		var body []byte
		if c.Request.Body != nil {
			b, err := io.ReadAll(c.Request.Body)
			if err == nil {
				body = b
			}
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

		// 2. Extraer campos anti-bot. Body vacío o no-JSON → extract en cero →
		//    pasa (el handler validará el payload real después).
		var extract honeypotExtract
		_ = json.Unmarshal(body, &extract)

		// 3. Campo trampa lleno → bot.
		if strings.TrimSpace(extract.Website) != "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "No pudimos procesar la solicitud. Intenta de nuevo.",
			})
			return
		}

		// 4. Envío instantáneo → bot (solo si el cliente envió la medición).
		if extract.FormElapsedMs != nil && *extract.FormElapsedMs >= 0 && *extract.FormElapsedMs < minFillMs {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "No pudimos procesar la solicitud. Intenta de nuevo.",
			})
			return
		}

		c.Next()
	}
}
