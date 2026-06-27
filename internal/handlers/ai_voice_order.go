// Spec: specs/085-vender-por-voz/spec.md
package handlers

import (
	"context"
	"io"
	"net/http"
	"time"

	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
)

// VoiceOrder — POST /api/v1/ai/voice-order (PremiumAuth). Recibe multipart con
// `audio_file` y devuelve {data: {commands, transcript, clarify_prompt, degraded}}.
// FAIL-SAFE: ante IA caída/timeout/JSON inválido responde 200 con degraded=true
// y commands vacío — NUNCA rompe la venta; el front cae a edición manual.
func VoiceOrder(geminiSvc *services.GeminiService) gin.HandlerFunc {
	degraded := func(c *gin.Context, reason string) {
		c.JSON(http.StatusOK, gin.H{"data": services.VoiceOrderResult{
			Commands: []services.VoiceOrderCommand{},
			Degraded: true,
			Reason:   reason,
		}})
	}
	return func(c *gin.Context) {
		if geminiSvc == nil {
			degraded(c, "ai_unavailable")
			return
		}

		file, header, err := c.Request.FormFile("audio_file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "archivo de audio requerido (campo: audio_file)",
			})
			return
		}
		defer file.Close()

		if header.Size <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "archivo vacío — grabe al menos un segundo",
			})
			return
		}
		if header.Size > maxVoiceAudioBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "el audio supera el máximo de 10MB",
			})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		if !services.IsSupportedAudioMimeType(mimeType) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "formato de audio no soportado",
				"error_code": "unsupported_audio_type",
				"received":   mimeType,
			})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo leer el audio"})
			return
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), middleware.GetTenantID(c)),
			45*time.Second,
		)
		defer cancel()

		res, err := geminiSvc.ExtractVoiceOrder(ctx, data, mimeType)
		if err != nil {
			// Fail-safe: no rompemos la venta — el front edita manual.
			degraded(c, "ai_error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": res})
	}
}
