// Spec: specs/043-menu-restaurante-recetas/spec.md
package handlers

import (
	"context"
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
