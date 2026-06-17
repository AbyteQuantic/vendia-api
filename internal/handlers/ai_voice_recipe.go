// Spec: specs/065-recipe-studio/spec.md
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

// VoiceRecipe is mounted under /api/v1/ai/voice-recipe (PremiumAuth).
// Accepts multipart/form-data with an `audio_file` field and returns the
// structured recipe `{"data": {name, ingredients[], steps[], ...}}` so the
// Recipe Studio opens prefilled for the user to review/edit. Mirrors
// VoiceInventory (same audio plumbing + guards).
func VoiceRecipe(geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}

		file, header, err := c.Request.FormFile("audio_file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "archivo de audio requerido (campo: audio_file)"})
			return
		}
		defer file.Close()

		if header.Size <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "archivo vacío — grabe al menos un segundo"})
			return
		}
		if header.Size > maxVoiceAudioBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el audio supera el máximo de 10MB"})
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

		recipe, err := geminiSvc.ExtractVoiceRecipe(ctx, data, mimeType)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "no se pudo interpretar el audio. Intente grabar de nuevo, más cerca del micrófono.",
				"detail": err.Error(),
			})
			return
		}
		if recipe.Name == "" && len(recipe.Ingredients) == 0 && len(recipe.Steps) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "no identificamos una receta en el audio. Diga el nombre del plato, los ingredientes y los pasos.",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": recipe})
	}
}

// RecipeAssist is mounted under /api/v1/ai/recipe-assist (PremiumAuth).
// Text assistant: completa o refina una receta. Body JSON:
//
//	{ "name": "...", "instructions": "hazla más económica",
//	  "current": { ...VoiceRecipeResult... } }
//
// Devuelve `{"data": {...VoiceRecipeResult...}}` para precargar el Studio.
func RecipeAssist(geminiSvc *services.GeminiService) gin.HandlerFunc {
	type Request struct {
		Name         string                     `json:"name"`
		Instructions string                     `json:"instructions"`
		Current      services.VoiceRecipeResult `json:"current"`
	}
	return func(c *gin.Context) {
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// Necesitamos al menos un nombre o un borrador para trabajar.
		if req.Name == "" && req.Current.Name == "" && len(req.Current.Ingredients) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "indique el nombre del plato para que la IA pueda ayudar",
			})
			return
		}
		name := req.Name
		if name == "" {
			name = req.Current.Name
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), middleware.GetTenantID(c)),
			45*time.Second,
		)
		defer cancel()

		recipe, err := geminiSvc.GenerateRecipeDraft(ctx, name, req.Instructions, req.Current)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "la IA no pudo armar la receta. Intente de nuevo.",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": recipe})
	}
}
