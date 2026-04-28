package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// PreviewLogoIA generates a logo for an UNREGISTERED merchant during
// onboarding step 5, BEFORE the tenant exists. The returned URL is
// then sent in the register payload (business.logo_url) so the
// tenant lands on the dashboard with its mark already in place.
//
// This route is public (no JWT) so the rate limiter on the calling
// group must be strict — generating images is the most expensive
// operation in the API and an unauthenticated caller could burn the
// Gemini quota in seconds. Production wires this through the same
// loginLimiter (5 req/min/IP) which is more than enough for a real
// onboarding (~3-4 generates max).
//
// POST /api/v1/auth/preview-logo
// body: {business_name, business_type, details}
// 200: {data: {logo_url}}
// 400: details too short / missing
// 503: Gemini or storage unavailable
func PreviewLogoIA(geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "servicio de IA no configurado",
			})
			return
		}
		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "servicio de almacenamiento no configurado",
			})
			return
		}

		var req struct {
			BusinessName string `json:"business_name"`
			BusinessType string `json:"business_type"`
			Details      string `json:"details"`
		}
		_ = c.ShouldBindJSON(&req)

		details := strings.TrimSpace(req.Details)
		if len([]rune(details)) < minLogoDetailsLength {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "describa su negocio (mínimo 12 caracteres) para que la IA acierte",
				"error_code": "logo_details_required",
			})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()

		logos, err := geminiSvc.GenerateLogo(ctx,
			req.BusinessName, req.BusinessType, details)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("error al generar logo: %v", err),
			})
			return
		}
		if len(logos) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "la IA no generó ninguna imagen",
			})
			return
		}

		// Upload to a "previews" namespace inside the same bucket. No
		// tenant id available yet, so we key on a random uuid; the
		// final tenant row references this URL on registration.
		// Orphans (merchant abandons onboarding) age out via bucket
		// lifecycle policy at the storage layer.
		logo := logos[0]
		key := fmt.Sprintf("logos/preview/%s.webp", uuid.NewString())
		logoURL, err := storageSvc.Upload(ctx, "store-logos",
			key, logo.ImageData, "image/webp")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al guardar logo generado",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"logo_url": logoURL}})
	}
}

// PreviewLogoUpload accepts a multipart upload from the same
// pre-register flow as PreviewLogoIA. Same rate-limiting story.
//
// POST /api/v1/auth/preview-logo-upload
// form: logo (file, ≤ 2 MB)
// 200: {data: {logo_url}}
func PreviewLogoUpload(storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "servicio de almacenamiento no configurado",
			})
			return
		}

		file, header, err := c.Request.FormFile("logo")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "logo requerido (campo: logo)",
			})
			return
		}
		defer file.Close()

		if header.Size > 2<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "logo excede 2MB"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al leer logo",
			})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		key := fmt.Sprintf("logos/preview/%s",
			strings.ReplaceAll(header.Filename, " ", "-"))
		// Always namespace with a uuid so two merchants picking the
		// same filename don't overwrite each other.
		key = fmt.Sprintf("logos/preview/%s-%s",
			uuid.NewString()[:8],
			strings.ReplaceAll(header.Filename, " ", "-"))

		logoURL, err := storageSvc.Upload(c.Request.Context(),
			"store-logos", key, data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al subir logo",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"logo_url": logoURL}})
	}
}
