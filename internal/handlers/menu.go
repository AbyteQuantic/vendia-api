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
	"github.com/google/uuid"
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

// GenerateMenuImage — POST /api/v1/menu/generate-image. Genera una foto de
// MUESTRA del plato con IA a partir del nombre (name-based, sin crear ningún
// producto — concilio 2026-06-13 opción C: evita el "first write wins" y los
// productos fantasma). Devuelve la URL en R2; el editor la guarda como un
// campo más del plato y la incluye en el createProduct del "Publicar".
//
// Síncrono como /menu/scan-photo (mismo patrón probado en producción): la
// llamada a Gemini tarda ~20-40s, dentro del timeout. Si en el futuro
// aparecen gateway-timeouts, migrar al patrón job+polling de Spec 016.
func GenerateMenuImage(geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if geminiSvc == nil || storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicios de IA no configurados"})
			return
		}

		var req struct {
			Name         string `json:"name"`
			Category     string `json:"category"`
			Description  string `json:"description"`
			Presentation string `json:"presentation"`
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

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID), 90*time.Second)
		defer cancel()

		// La muestra se basa en nombre + descripción (ingredientes) + cómo se
		// sirve (presentación), para que la foto sea mucho más certera (F043).
		img, err := geminiSvc.GenerateDishImage(
			ctx, name, strings.TrimSpace(req.Description), strings.TrimSpace(req.Presentation))
		if err != nil || len(img) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "No pudimos crear la foto. Intenta de nuevo.",
			})
			return
		}

		key := fmt.Sprintf("menu/%s/%s.png", tenantID, uuid.NewString())
		url, err := storageSvc.Upload(ctx, "product-photos", key, img, "image/png")
		if err != nil || url == "" {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "No pudimos guardar la foto. Intenta de nuevo.",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"image_url": url}})
	}
}

// EnhanceMenuImage — POST /api/v1/menu/enhance-image. Recibe la foto REAL del
// plato (multipart, campo `image`) y la mejora con IA de forma FIEL (Spec 017,
// EnhancePhoto): recorta el fondo, la pone sobre blanco con luz de estudio y
// limpia la superficie SIN redibujar el plato ni cambiar sus rasgos — así el
// comensal ve el plato real del local, solo mejor fotografiado (no se le
// engaña). Espejo stateless de GenerateMenuImage: no crea producto, sube a R2 y
// devuelve la URL; el plato-borrador la guarda y la publica al "Publicar".
//
// Síncrono con timeout de 90s (mismo patrón probado de generate-image/scan). Si
// aparecen gateway-timeouts en producción, migrar a un job+polling stateless
// keyed por draft_id (NUNCA product_id — rompería "sin productos fantasma").
func EnhanceMenuImage(geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if geminiSvc == nil || storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicios de IA no configurados"})
			return
		}

		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: image)"})
			return
		}
		defer file.Close()

		if header.Size > 8<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "La foto es muy pesada, intente con otra"})
			return
		}
		// El mimeType viaja tal cual del archivo subido (image/jpeg, png, HEIC):
		// EnhancePhoto lo respeta. No asumir jpeg ciegamente (bug clase F010 HEIC).
		mimeType := header.Header.Get("Content-Type")
		if !strings.HasPrefix(mimeType, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el archivo debe ser una imagen"})
			return
		}

		name := strings.TrimSpace(c.PostForm("name"))
		if len(name) < 2 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "escribe el nombre del plato primero"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer la imagen"})
			return
		}
		if len(data) > 8<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "La foto es muy pesada, intente con otra"})
			return
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID), 90*time.Second)
		defer cancel()

		// name como contexto (productInfo): pista de qué es el objeto, NUNCA un
		// objetivo de generación — la foto adjunta manda (FR-017, fidelidad).
		img, err := geminiSvc.EnhancePhoto(ctx, data, mimeType, name)
		if err != nil || len(img) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "No pudimos mejorar la foto. Intenta de nuevo.",
			})
			return
		}

		key := fmt.Sprintf("menu/%s/%s.png", tenantID, uuid.NewString())
		url, err := storageSvc.Upload(ctx, "product-photos", key, img, "image/png")
		if err != nil || url == "" {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "No pudimos guardar la foto. Intenta de nuevo.",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"image_url": url}})
	}
}
