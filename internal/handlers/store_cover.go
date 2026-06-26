// Spec: specs/082-catalogo-online-personalizacion/spec.md
package handlers

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GenerateStoreCover — POST /api/v1/store/cover-ai
//
// Portada del catálogo con IA (Spec 082 F2b). Dos modos en un solo endpoint:
//   - SIN imagen → genera una portada desde cero (nombre + tipo de negocio).
//   - CON imagen (multipart "image") → MEJORA la que el tendero subió.
// Devuelve {cover_url}; la app la guarda en store_cover_url al "Guardar".
func GenerateStoreCover(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IA no disponible"})
			return
		}

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		// Imagen opcional a MEJORAR.
		var refImages []services.ReferenceImage
		enhancing := false
		if file, header, err := c.Request.FormFile("image"); err == nil {
			defer file.Close()
			data, _ := io.ReadAll(io.LimitReader(file, 5<<20))
			if len(data) > 0 {
				mime := header.Header.Get("Content-Type")
				if mime == "" {
					mime = "image/jpeg"
				}
				refImages = append(refImages, services.ReferenceImage{MimeType: mime, Data: data})
				enhancing = true
			}
		}

		types := strings.Join(tenant.BusinessTypes, ", ")
		var prompt string
		if enhancing {
			prompt = fmt.Sprintf(
				"Mejora y limpia esta foto para usarla como PORTADA horizontal (16:9) del "+
					"catálogo en línea de la tienda \"%s\" (%s). Más nítida, bien iluminada, "+
					"colores vivos, SIN texto ni logos. Conserva el tema de la imagen.",
				tenant.BusinessName, types)
		} else {
			prompt = fmt.Sprintf(
				"Crea una PORTADA horizontal (16:9) moderna y atractiva para el catálogo en "+
					"línea de la tienda \"%s\" (%s) en Colombia. Estilo limpio, colores vivos, "+
					"que represente el tipo de negocio. SIN texto ni letras.",
				tenant.BusinessName, types)
		}

		ctx := c.Request.Context()
		imageBytes, err := geminiSvc.GeneratePromoBanner(ctx, prompt, refImages)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo generar la portada", "detail": err.Error()})
			return
		}

		var url string
		if storageSvc != nil {
			key := fmt.Sprintf("%s/cover-%s.png", tenantID, uuid.NewString())
			uploaded, upErr := storageSvc.Upload(ctx, "promo-banners", key, imageBytes, "image/png")
			if upErr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo guardar la portada"})
				return
			}
			url = uploaded
		} else {
			url = "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"cover_url": url}})
	}
}
