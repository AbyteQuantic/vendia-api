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
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// minLogoDetailsLength mirrors the UI threshold in step_logo.dart so
// frontend and backend agree on what "enough description" means.
// Counted in runes (not bytes) so accented characters count as one.
const minLogoDetailsLength = 12

func GenerateLogo(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}
		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}

		// Accept JSON body with business_name / business_type / details
		var req struct {
			BusinessName string `json:"business_name"`
			BusinessType string `json:"business_type"`
			// Details — free-text the merchant typed in the onboarding
			// logo step describing what makes their business special.
			// Folded into the IA prompt as a "Brand tone" line so the
			// model picks symbology / palette accents that match what
			// they actually sell, instead of a generic rubro icon.
			Details string `json:"details"`
		}
		_ = c.ShouldBindJSON(&req)

		// Reject calls without a meaningful brand tone — the UI gate is
		// the friendlier first line of defense, this is the contract
		// at the API boundary so a stale client / curl probe can't
		// burn a Gemini credit on an empty prompt.
		details := strings.TrimSpace(req.Details)
		if len([]rune(details)) < minLogoDetailsLength {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "describa su negocio (mínimo 12 caracteres) para que la IA acierte",
				"error_code": "logo_details_required",
			})
			return
		}
		req.Details = details

		// Fallback to tenant data if not provided
		if req.BusinessName == "" || req.BusinessType == "" {
			var tenant models.Tenant
			if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
				return
			}
			if req.BusinessName == "" {
				req.BusinessName = tenant.BusinessName
			}
			if req.BusinessType == "" && len(tenant.BusinessTypes) > 0 {
				req.BusinessType = tenant.BusinessTypes[0]
			}
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID),
			60*time.Second,
		)
		defer cancel()

		logos, err := geminiSvc.GenerateLogo(ctx,
			req.BusinessName, req.BusinessType, req.Details)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al generar logo: %v", err)})
			return
		}
		if len(logos) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "la IA no generó ninguna imagen"})
			return
		}

		// Upload to store-logos bucket in Supabase
		logo := logos[0]
		key := fmt.Sprintf("logos/%s/ai-%s.webp", tenantID, uuid.NewString()[:8])
		logoURL, err := storageSvc.Upload(ctx, "store-logos", key, logo.ImageData, "image/webp")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar logo generado"})
			return
		}

		// Persist logo_url on tenant
		db.Model(&models.Tenant{}).Where("id = ?", tenantID).Update("logo_url", logoURL)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"logo_url": logoURL}})
	}
}

func UploadLogo(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		file, header, err := c.Request.FormFile("logo")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "logo requerido (campo: logo)"})
			return
		}
		defer file.Close()

		if header.Size > 2<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "logo excede 2MB"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer logo"})
			return
		}

		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}

		// Feature 010: sniff the real image format from the bytes
		// instead of trusting the client Content-Type. iPhone photos
		// arrive as HEIC; uploading HEIC to the logos bucket fails with
		// a generic 500. Detect it here and reject with a clear 400.
		mimeType := detectImageType(data)
		if !uploadableImageTypes[mimeType] {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      logoFormatoNoSoportadoMsg,
				"error_code": logoFormatoNoSoportadoCode,
			})
			return
		}
		key := fmt.Sprintf("logos/%s/custom-%s.webp", tenantID, uuid.NewString()[:8])

		logoURL, err := storageSvc.Upload(c.Request.Context(), "vendia-logos", key, data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al subir logo"})
			return
		}

		db.Model(&models.Tenant{}).Where("id = ?", tenantID).Update("logo_url", logoURL)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"logo_url": logoURL}})
	}
}
