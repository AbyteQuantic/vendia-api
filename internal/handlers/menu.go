// Spec: specs/043-menu-restaurante-recetas/spec.md
package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
)

// ScanMenu — POST /api/v1/menu/scan-photo. Lee una foto de la CARTA/MENÚ del
// restaurante con IA y devuelve los platos extraídos (nombre, descripción,
// precio, porción, categoría) para que el tendero los revise/edite antes de
// publicarlos. No persiste nada: el guardado lo hace el cliente plato por plato
// vía CreateProduct (is_menu_item=true).
func ScanMenu(geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}

		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: image)"})
			return
		}
		defer file.Close()

		if header.Size > 8<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la imagen excede 8MB"})
			return
		}
		mimeType := header.Header.Get("Content-Type")
		if !strings.HasPrefix(mimeType, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el archivo debe ser una imagen"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer la imagen"})
			return
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID), 45*time.Second)
		defer cancel()

		result, err := geminiSvc.ScanMenu(ctx, data, mimeType)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "No pudimos leer tu menú. Toma la foto con buena luz y que se vean los platos.",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"dishes": result.Dishes}})
	}
}

// GenerateMenuDescription — POST /api/v1/menu/generate-description. Recibe
// el nombre (y opcionalmente la categoría) de un plato y devuelve una
// descripción corta y apetecible para el menú público, generada con IA.
// Síncrono (texto, no imagen): el tendero la recibe al instante y la edita.
func GenerateMenuDescription(geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}

		var req struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nombre del plato requerido"})
			return
		}
		name := strings.TrimSpace(req.Name)
		if len(name) < 2 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "escribe el nombre del plato primero"})
			return
		}

		prompt := fmt.Sprintf(
			`Eres el ayudante de un restaurante colombiano. Escribe UNA descripción corta y apetecible (máximo 18 palabras, una sola frase) para el plato del menú llamado "%s"%s.
REGLAS:
- Español neutro, claro, sin jerga ni voseo.
- Menciona ingredientes o preparación típicos del plato SOLO si son obvios por el nombre; si no estás seguro, describe de forma general y apetecible sin inventar ingredientes específicos.
- NADA de precios, ni emojis, ni comillas, ni la palabra "descripción".
- Devuelve SOLO la frase, sin nada más.`,
			name,
			func() string {
				if c := strings.TrimSpace(req.Category); c != "" {
					return fmt.Sprintf(" (categoría: %s)", c)
				}
				return ""
			}(),
		)

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID), 30*time.Second)
		defer cancel()

		text, err := geminiSvc.GenerateText(ctx, prompt)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "No pudimos generar la descripción. Intenta de nuevo.",
			})
			return
		}
		// Limpieza defensiva: una sola línea, sin comillas envolventes.
		desc := strings.TrimSpace(text)
		desc = strings.Trim(desc, "\"'")
		if i := strings.IndexAny(desc, "\n\r"); i >= 0 {
			desc = strings.TrimSpace(desc[:i])
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"description": desc}})
	}
}
